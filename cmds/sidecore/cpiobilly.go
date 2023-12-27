// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	"github.com/u-root/cpu/client"
	"github.com/u-root/u-root/pkg/cpio"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

type no struct{}

// Chroot
func (*no) Chroot(_ string) (billy.Filesystem, error) {
	return nil, os.ErrInvalid
}

func (*fsCPIO) Root() string {
	return "/" // not os.PathSeparator; this is cpio.
}

func (*no) Create(filename string) (billy.File, error) { return nil, os.ErrInvalid }
func (*no) Stat(filename string) (os.FileInfo, error)  { panic("stat"); return nil, os.ErrInvalid }
func (*no) Rename(oldpath, newpath string) error       { return os.ErrPermission }
func (*no) Remove(filename string) error               { return os.ErrPermission }
func (*no) Join(elem ...string) string                 { return path.Join(elem...) }

// TempFile
func (*no) TempFile(dir, prefix string) (billy.File, error) { return nil, os.ErrPermission }

// Dir
func (*no) ReadDir(path string) ([]os.FileInfo, error)       { panic("readdir"); return nil, os.ErrInvalid }
func (*no) MkdirAll(filename string, perm os.FileMode) error { return os.ErrPermission }

// Symlink
func (*no) Lstat(filename string) (os.FileInfo, error) { panic("Lstat"); return nil, os.ErrInvalid }
func (*no) Symlink(target, link string) error          { return os.ErrPermission }

// File
func (*no) Name() string              { panic("Name"); return "" }
func (*no) Lock() error               { return nil }
func (*no) Unlock() error             { return nil }
func (*no) Truncate(size int64) error { return os.ErrPermission }

// File IO -- most of these don't matter for NFS.
// We do not track position, b/c NFS always sends an offset.
type fileFail struct{}

func (*fileFail) Write(p []byte) (n int, err error) { panic("Write"); return -1, os.ErrPermission }
func (*fileFail) Read(p []byte) (n int, err error)  { panic("Read"); return -1, os.ErrPermission }
func (*fileFail) Seek(offset int64, whence int) (int64, error) {
	panic("Seek")
	return -1, os.ErrPermission
}

// The only one we will actually implement -- later.
func (*fileFail) ReadAt(p []byte, off int64) (n int, err error) {
	panic("ReadAt")
	return -1, os.ErrPermission
}

type ok struct{}

func (*ok) Close() error { return nil }

// fsCPIO implements fs.Stat
type fsCPIO struct {
	no
	file    *os.File
	rr      cpio.RecordReader
	m       map[string]uint64
	recs    []cpio.Record
	mnts    []MountPoint
	mntLock sync.RWMutex
}

type MountPoint struct {
	n  string
	fs billy.Filesystem
}

func (f *fsCPIO) hasMount(n string) (int, error) {
	f.mntLock.RLock()
	defer f.mntLock.RUnlock()
	for i, v := range f.mnts {
		if strings.HasPrefix(n, v.n) {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%s:%w", n, os.ErrNotExist)
}

// Mount adds a mountpoint to an fsCPIO
func (f *fsCPIO) Mount(name string, fs billy.Filesystem) error {
	f.mntLock.Lock()
	defer f.mntLock.Unlock()
	for _, m := range f.mnts {
		if strings.HasPrefix(name, m.n) {
			return fmt.Errorf("%q:%w", m.n, os.ErrExist)
		}
	}
	f.mnts = append(f.mnts, MountPoint{n: name, fs: fs})
	return nil
}

// Chroot
func (*fsCPIO) Chroot(_ string) (billy.Filesystem, error) {
	return nil, os.ErrPermission
}

// Create
func (*fsCPIO) Create(_ string) (billy.File, error) {
	return nil, os.ErrPermission
}

// ReadDir implements readdir for fsCPIO.
// If path is empty, ino 0 (root) is assumed.
func (f *fsCPIO) ReadDir(path string) ([]os.FileInfo, error) {
	ino, ok := f.m[path]
	verbose("fseraddr %q ino %d %v", path, ino, ok)
	if !ok {
		ino = 0
	}
	l := file{Path: ino, fs: f}
	fi, err := l.ReadDir(0, 1048576) // no idea what to do for size.
	verbose("%v, %v", fi, err)
	return fi, err
}

func (f *fsCPIO) Name() string {
	return f.recs[0].Name
}

func (f *fsCPIO) Size() int64 {
	return int64(f.recs[0].FileSize)
}

func uToGo(m uint64) os.FileMode {
	verbose("fsCPIO mode: %#x", m)
	// the billy API is in terms of go fs values.
	// We need to map types from Unix to go fs package.
	// Just hack this together for now, once it works,
	// we can figure out how to clean it all up.
	// arguably, cpio package should export its functions.
	// arguably, Go should too ...
	u := os.FileMode(m)
	perm := u & fs.ModePerm
	// we have to match bits that are not available on windows
	var t fs.FileMode
	switch u & 0170000 {
	case 0010000: //S_IFIFO * named pipe (fifo) */
		t = fs.ModeNamedPipe
	case 0020000: //S_IFCHR * character special */
		t = fs.ModeCharDevice
	case 0040000: //S_IFDIR * directory */
		t = fs.ModeDir
	case 0060000: //S_IFBLK * block special */
		t = fs.ModeDevice
	case 0100000: //S_IFREG * regular */
	case 0120000: //S_IFLNK * symbolic link */
		t = fs.ModeSymlink
	}
	verbose("Mode is %#x", perm|t)
	verbose("Mode is %v", os.FileMode(perm|t))
	return os.FileMode(perm | t)
}

func (f *fsCPIO) Mode() os.FileMode {
	m := uToGo(f.recs[0].Mode)
	verbose("fsCPIO mode: %v %#x", m, uint64(m))
	return m
}

func (f *fsCPIO) ModTime() time.Time {
	return time.Unix(0, 0)
}

func (f *fsCPIO) IsDir() bool {
	verbose("fsCPIO mode: true")
	return true
}

func (f *fsCPIO) Sys() any {
	return nil
}

func (fs *fsCPIO) Readlink(link string) (string, error) {
	ino, ok := fs.m[link]
	if !ok {
		return "", os.ErrNotExist
	}
	f := &file{fs: fs, Path: ino}
	return f.Readlink()
}

var _ billy.Filesystem = &fsCPIO{}

// A file is a server and an index into the cpio records.
type file struct {
	no
	fileFail
	ok
	fs   *fsCPIO
	Path uint64
}

var _ billy.File = &file{}

// fstat implements fs.FileInfo. Arguably, cpio.Record should.
type fstat struct {
	*cpio.Record
}

func (f *fstat) Name() string {
	verbose("file Name(): rec %v", f.Record)
	return path.Base(f.Record.Name)
}

func (f *fstat) Size() int64 {
	return int64(f.FileSize)
}

func (f *fstat) Mode() os.FileMode {
	m := uToGo(f.Record.Mode)
	verbose("fstat mode: %v %#x", m, uint64(m))
	return m
}

func (f *fstat) ModTime() time.Time {
	return time.Unix(0, 0)
}

func (f *fstat) IsDir() bool {
	verbose("fstat mode: %v", f.Mode()&cpio.S_IFDIR == cpio.S_IFDIR)
	return f.Mode().IsDir()
}

func (f *fstat) Sys() any {
	return nil
}

// NewfsCPIO returns a fsCPIO, properly initialized.
func NewfsCPIO(c string) (*fsCPIO, error) {
	f, err := os.Open(c)
	if err != nil {
		return nil, err
	}

	archive, err := cpio.Format("newc")
	if err != nil {
		return nil, err
	}

	rr, err := archive.NewFileReader(f)
	if err != nil {
		return nil, err
	}

	recs, err := cpio.ReadAllRecords(rr)
	if len(recs) == 0 {
		return nil, fmt.Errorf("cpio:No records: %w", os.ErrInvalid)
	}

	if err != nil {
		return nil, err
	}

	m := map[string]uint64{}
	for i, r := range recs {
		v("put %s in %d", r.Info.Name, i)
		m[r.Info.Name] = uint64(i)
	}

	return &fsCPIO{file: f, rr: rr, recs: recs, m: m}, nil
}

func (fs *fsCPIO) Stat(filename string) (os.FileInfo, error) {
	verbose("fsCPIO stat %q", filename)
	if len(filename) == 0 {
		return &fstat{Record: &fs.recs[0]}, nil
	}
	ino, ok := fs.m[filename]
	verbose("fseraddr %q ino %d %v", filename, ino, ok)
	if !ok {
		return nil, os.ErrNotExist
	}
	return &fstat{Record: &fs.recs[ino]}, nil
}

func (l *file) rec() (*cpio.Record, error) {
	if int(l.Path) > len(l.fs.recs) {
		return nil, os.ErrNotExist
	}
	v("cpio:rec for %v is %v", l, l.fs.recs[l.Path])
	return &l.fs.recs[l.Path], nil
}

func (f *fsCPIO) lookup(filename string) (*cpio.Record, error) {
	ino, ok := f.m[filename]
	verbose("fseraddr %q ino %d %v", filename, ino, ok)
	if !ok {
		ino = 0
	}
	l := &file{Path: ino, fs: f}
	return l.rec()
}

func (fs *fsCPIO) Open(filename string) (billy.File, error) {
	ino, ok := fs.m[filename]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &file{fs: fs, Path: ino}, nil
}

func (*no) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	panic("openfile")
	return nil, os.ErrPermission
}

// Read implements p9.File.ReadAt.
func (l *file) ReadAt(p []byte, offset int64) (int, error) {
	r, err := l.rec()
	if err != nil {
		return -1, err
	}
	return r.ReadAt(p, offset)
}

// Write implements p9.File.WriteAt.
func (l *file) WriteAt(p []byte, offset int64) (int, error) {
	return -1, os.ErrPermission
}

// readdir returns a slice of indices for a directory.
// See commend below as to why it must be a slice, not a range.
func (l *file) readdir() ([]uint64, error) {
	verbose("file:readdir at %d", l.Path)
	r, err := l.rec()
	if err != nil {
		return nil, err
	}
	dn := r.Info.Name
	verbose("cpio:readdir starts from %v %v", l, r)
	// while the name is a prefix of the records we are scanning,
	// append the record.
	// This can not be returned as a range as we do not want
	// contents of all subdirs.
	var list []uint64
	for i, r := range l.fs.recs[l.Path+1:] {
		// filepath.Rel fails, we're done here.
		b, err := filepath.Rel(dn, r.Name)
		if err != nil {
			verbose("cpio:r.Name %q: DONE", r.Name)
			break
		}
		dir, _ := filepath.Split(b)
		if len(dir) > 0 {
			continue
		}
		verbose("cpio:readdir: %v", i)
		list = append(list, uint64(i)+l.Path+1)
	}
	return list, nil
}

// ReadDir implements ReadDir.
// This is a bit of a mess in cpio, but the good news is that
// files will be in some sort of order ...
func (l *file) ReadDir(offset uint64, count uint32) ([]fs.FileInfo, error) {
	verbose("file readdir")
	if _, err := l.rec(); err != nil {
		return nil, err
	}
	list, err := l.readdir()
	if err != nil {
		return nil, err
	}
	if offset > uint64(len(list)) {
		return nil, io.EOF
	}
	// NOTE: go-nfs takes care of . and .., so it is ok to skip it here.
	verbose("cpio:readdir list %v", list)
	dirents := make([]os.FileInfo, 0, len(list))
	//verbose("cpio:readdir %q returns %d entries start at offset %d", l.Path, len(fi), offset)
	for _, i := range list[offset:] {
		entry := file{Path: i + offset, fs: l.fs}
		r, err := entry.rec()
		if err != nil {
			continue
		}
		verbose("cpio:add path %d %q", i+offset, filepath.Base(r.Info.Name))
		dirents = append(dirents, &fstat{Record: r})
	}

	verbose("cpio:readdir:return %v, nil", dirents)
	return dirents, nil

}

// Readlink implements p9.File.Readlink.
func (l *file) Readlink() (string, error) {
	v("cpio:readlinkat:%v", l)
	r, err := l.rec()
	if err != nil {
		return "", err
	}
	link := make([]byte, r.FileSize, r.FileSize)
	v("cpio:readlink: %d byte link", len(link))
	if n, err := r.ReadAt(link, 0); err != nil || n != len(link) {
		v("cpio:readlink: fail with (%d,%v)", n, err)
		return "", err
	}
	v("cpio:readlink: %q", string(link))
	return string(link), nil
}

// ROFS is an intercepter for the filesystem indicating it should
// be read only. The undelrying billy.Memfs indicates it supports
// writing, but does not in implement billy.Change to support
// modification of permissions / modTimes, and as such cannot be
// used as RW system.
type ROFS struct {
	billy.Filesystem
}

// Capabilities exports the filesystem as readonly
func (ROFS) Capabilities() billy.Capability {
	return billy.ReadCapability
}

func srvNFS(cl *client.Cmd, n string) (func() error, string, error) {
	mem, err := NewfsCPIO(n)
	if err != nil {
		return nil, "", err
	}
	l, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// If ipv4 isn't available, try ipv6.  It's not enough
		// to use Listen("tcp", "localhost:0a)", since we (the
		// cpu client) might have v4 (which the runtime will
		// use if we say "localhost"), but the server (cpud)
		// might not.
		l, err = cl.Listen("tcp", "[::1]:0")
		if err != nil {
			return nil, "", fmt.Errorf("cpu client listen for forwarded nfs port %v", err)
		}
	}
	log.Printf("ssh.listener %v", l.Addr().String())
	ap := strings.Split(l.Addr().String(), ":")
	if len(ap) == 0 {
		return nil, "", fmt.Errorf("Can't find a port number in %v", l.Addr().String())
	}
	portnfs, err := strconv.ParseUint(ap[len(ap)-1], 0, 16)
	if err != nil {
		return nil, "", fmt.Errorf("Can't find a 16-bit port number in %v", l.Addr().String())
	}
	verbose("listener %T %v addr %v port %v", l, l, l.Addr().String(), portnfs)

	u, err := uuid.NewRandom()
	if err != nil {
		return nil, "", err
	}
	handler := NewNullAuthHandler(l, ROFS{mem}, u.String())
	log.Printf("uuid is %q", u.String())
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)
	f := func() error {
		return nfs.Serve(l, cacheHelper)
	}
	fstab := fmt.Sprintf("127.0.0.1:%s /tmp/cpu nfs ro,relatime,vers=3,rsize=1048576,wsize=1048576,namlen=255,hard,nolock,proto=tcp,port=%d,timeo=600,retrans=2,sec=sys,mountaddr=127.0.0.1,mountvers=3,mountport=%d,mountproto=tcp,local_lock=all,addr=127.0.0.1 0 0\n", u, portnfs, portnfs)
	return f, fstab, nil
}

// auth handler for our special sauce.

// NewNullAuthHandler creates a handler for the provided filesystem
// todo: youbetter becomes a nonce
func NewNullAuthHandler(l net.Listener, fs billy.Filesystem, nonce string) nfs.Handler {
	return &NullAuthHandler{l: l, fs: fs, n: nonce}
}

// NullAuthHandler returns a NFS backing that exposes a given file system in response to all mount requests.
type NullAuthHandler struct {
	l     net.Listener
	count int32
	fs    billy.Filesystem
	n     string
}

// Mount backs Mount RPC Requests, allowing for access control policies.
func (h *NullAuthHandler) Mount(ctx context.Context, conn net.Conn, req nfs.MountRequest) (status nfs.MountStatus, hndl billy.Filesystem, auths []nfs.AuthFlavor) {
	// "Give me a ping, Vasili. One ping only, please."
	// Even if it fails, you only get one chance.
	c := atomic.AddInt32(&h.count, 1)
	if c > 1 {
		status = nfs.MountStatusErrPerm
		return
	}
	if string(req.Dirpath) != h.n {
		status = nfs.MountStatusErrNoEnt
		verbose("req.Dirpath %q != nonce %q", string(req.Dirpath), h.n)
		return
	}

	status = nfs.MountStatusOk
	hndl = h.fs
	auths = []nfs.AuthFlavor{nfs.AuthFlavorNull}
	return
}

// Change provides an interface for updating file attributes.
func (h *NullAuthHandler) Change(fs billy.Filesystem) billy.Change {
	if c, ok := h.fs.(billy.Change); ok {
		return c
	}
	return nil
}

// FSStat provides information about a filesystem.
func (h *NullAuthHandler) FSStat(ctx context.Context, f billy.Filesystem, s *nfs.FSStat) error {
	return nil
}

// ToHandle handled by CachingHandler
func (h *NullAuthHandler) ToHandle(f billy.Filesystem, s []string) []byte {
	return []byte{}
}

// FromHandle handled by CachingHandler
func (h *NullAuthHandler) FromHandle([]byte) (billy.Filesystem, []string, error) {
	return nil, []string{}, nil
}

// HandleLImit handled by cachingHandler
func (h *NullAuthHandler) HandleLimit() int {
	return -1
}
