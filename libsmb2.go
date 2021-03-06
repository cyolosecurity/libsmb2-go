package libsmb2

import (
	"errors"
	"fmt"
	"io"
	"os"
	path2 "path"
	"sync"
	"time"
	"unsafe"
)
//#include "libsmb2go.h"
import "C"

type Smb struct {
	session *C.struct_smb2_context
	connected bool
	mutex  sync.Mutex
}

type cSmbStat struct {
	name	string
	smbStat C.struct_smb2_stat_64
}

type smbStat struct {
	name string
	isDir bool
	modTime time.Time
	mode os.FileMode
	size int64
}

type smbFile struct {
	smb		*Smb
	fd		*C.struct_smb2fh
	dir		*C.struct_smb2dir
	path	string
	pos		int64
	*smbStat
	mutex  sync.Mutex
}

func NewSmb() *Smb {
	res := &Smb{
		session: C.smb2_init_context(),
	}
	return res
}

func (s *Smb) Connect(host string, share string, user string, password string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	C.smb2_set_user(s.session, C.CString(user))
	C.smb2_set_password(s.session, C.CString(password))

	if code := C.smb2_connect_share(s.session, C.CString(host), C.CString(share), C.CString(user)); code == 0 {
		s.connected = true
		return nil
	} else {
		s.disconnect()
		return errors.New(fmt.Sprintf("unable to connect to %s, code %d, %s", host, int(code), C.GoString(C.smb2_get_error(s.session))))
	}
}

func (s *Smb) disconnect() {
	if s.session != nil {
		if s.connected {
			C.smb2_disconnect_share(s.session)
		}
		C.smb2_destroy_context(s.session)
		s.session = nil
	}
}

func (s* Smb) Disconnect() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.disconnect()
}


func (s* Smb) OpenFile(path string, mode int) (*smbFile, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.session == nil {
		return nil, errors.New("opening file on closed session")
	}
	file := &smbFile{
		smb: s,
		path: path,
	}
	if file.fd = C.smb2_open(s.session, C.CString(path), C.int(mode)); file.fd == nil {
		if file.dir = C.smb2_opendir(s.session, C.CString(path)); file.dir == nil {
			return nil, errors.New(fmt.Sprintf("file open failed "+C.GoString(C.smb2_get_error(s.session))))
		} else {
			file.smbStat=&smbStat{}
			file.smbStat.isDir = true
			file.smbStat.name = path2.Base(path)
			file.smbStat.modTime = time.Now()
		}
	} else {
		st := cSmbStat{name: path2.Base(path)}
		C.smb2_fstat(s.session, file.fd, &st.smbStat)
		file.smbStat = st.toGoStat()
	}
	return file, nil
}

func (f *smbFile) Read(p []byte) (n int, err error) {
	f.smb.mutex.Lock()
	defer f.smb.mutex.Unlock()
	if f.fd == nil || f.smb.session == nil {
		return 0, io.EOF
	}
	n=int(C.smb2_read_wrapper(f.smb.session, f.fd, unsafe.Pointer(&p[0]), C.ulong(len(p)), C.longlong(f.pos)))
	if n <= 0 {
		err=io.EOF
	} else {
		f.pos+=int64(n)
	}
	return
}

func (f *smbFile) Write(p []byte) (n int, err error) {
	f.smb.mutex.Lock()
	defer f.smb.mutex.Unlock()
	if f.fd == nil || f.smb.session == nil {
		return 0, io.EOF
	}
	n=int(C.smb2_write_wrapper(f.smb.session, f.fd, unsafe.Pointer(&p[0]), C.ulong(len(p))));
	if n <= 0 {
		err = errors.New("write error "+C.GoString(C.smb2_get_error(f.smb.session)))
	}
	return
}

func (f *smbFile) Stat() (os.FileInfo, error) {
	return f, nil
}

func (f *smbFile) Seek(offset int64, whence int) (res int64, err error){
	f.smb.mutex.Lock()
	defer f.smb.mutex.Unlock()
	if f.fd == nil || f.smb.session == nil {
		return 0, io.EOF
	}
	realOffset := offset
	if whence == io.SeekEnd {
		realOffset = f.Size() + offset
		whence = io.SeekStart
	}
	res = int64(C.smb2_lseek_wrapper(f.smb.session, f.fd, C.longlong(realOffset), C.int(whence)))
	if res < 0 {
		err = errors.New("seek error: "+C.GoString(C.smb2_get_error(f.smb.session)))
	} else {
		f.pos = res
	}
	return
}

func (f *smbFile) Readdir(count int) (infos []os.FileInfo, err error) {
	f.smb.mutex.Lock()
	defer f.smb.mutex.Unlock()
	list := C.smb2_opendir(f.smb.session, C.CString(f.path))
	defer C.smb2_closedir(f.smb.session, list)
	infos=make([]os.FileInfo, 0)
	ent := C.smb2_readdir(f.smb.session, list)
	for i:=0; ent!=nil && ( count <= 0 || i<count); i++ {
		st := cSmbStat{name: C.GoString(ent.name), smbStat: ent.st}
		infos = append(infos, st.toGoStat())
		ent = C.smb2_readdir(f.smb.session, list)
	}
	if len(infos) < 1 {
		err = io.EOF
	}
	return
}

func (f *smbFile) Close() error {
	f.smb.mutex.Lock()
	defer f.smb.mutex.Unlock()
	if f.fd == nil || f.smb.session == nil {
		return nil
	}
	if f.fd != nil {
		C.smb2_close(f.smb.session, f.fd)
	} else if f.dir != nil {
		C.smb2_closedir(f.smb.session, f.dir)
	}
	f.fd = nil
	return nil
}

func (f *cSmbStat) Name() string {
	return f.name
}

func (f *cSmbStat) IsDir() bool {
	return os.FileMode(uint32(f.smbStat.smb2_type)).IsDir()
}

func (f *cSmbStat) ModTime() time.Time {
	return time.Unix(int64(f.smbStat.smb2_mtime),0)
}

func (f *cSmbStat) Size() int64 {
	return int64(f.smbStat.smb2_size)
}

func (f *cSmbStat) Mode() os.FileMode {
	return 666
}

func (f *smbStat) Name() string {
	return f.name
}

func (f *smbStat) IsDir() bool {
	return f.isDir
}

func (f *smbStat) ModTime() time.Time {
	return f.modTime
}

func (f *smbStat) Size() int64 {
	return f.size
}

func (f *smbStat) Mode() os.FileMode {
	return f.mode
}

func (f *smbStat) Sys() interface{} {
	return nil
}

func (f *cSmbStat) toGoStat() *smbStat {
	return &smbStat{
		name:     f.Name(),
		isDir:    f.IsDir(),
		modTime:  f.ModTime(),
		mode:     f.Mode(),
		size:	  f.Size(),
	}
}

func (f *cSmbStat) Sys() interface{} {
	return nil
}




