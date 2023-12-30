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

// This is a big of an odd experiment.
// For 9p, we build a unionfs, and a cpiofs. But a cpiofs, most of the time,
// says "can't do that."
// So for this iteration, we made the cpiofs the "default" case for file
// operations, and put mounts "in front of" the cpio.
// Rather than having the cpio fail anything looking like a write, we
// embed a struct in the fs that performs those operations on the mounts.
// As noted, still an experiment, and whether the simplification of having
// one, not two, file systems is worht the increased complexity is an open
// question.
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	"github.com/u-root/cpu/client"
	"github.com/u-root/u-root/pkg/cpio"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

type no struct{}

// Chroot. This is deprecated, so we don't bother.
func (*fsCPIO) Chroot(_ string) (billy.Filesystem, error) {
	return nil, os.ErrInvalid
}

func (*fsCPIO) Root() string {
	return "/" // not os.PathSeparator; this is cpio.
}

// TempFile
func (*no) TempFile(dir, prefix string) (billy.File, error) { return nil, os.ErrPermission }

// Symlink
func (*no) Symlink(target, link string) error { return os.ErrPermission }

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
	file *os.File
	rr   cpio.RecordReader
	m    map[string]uint64
	recs []cpio.Record
	mnts []MountPoint
}

// MountPoint is a mountpiont in an fsCPIO
type MountPoint struct {
	n  string
	fs billy.Filesystem
}

func (f *fsCPIO) hasMount(n string) (*MountPoint, string, error) {
	for i, v := range f.mnts {
		if strings.HasPrefix(n, v.n) {
			rel, err := filepath.Rel(v.n, n)
			if err != nil {
				continue
			}
			return &f.mnts[i], rel, nil
		}
	}
	return nil, "", fmt.Errorf("%s:%w", n, os.ErrNotExist)
}

// mount adds a mountpoint to an fsCPIO.
// It is only intended to be called from New, and only checks
// for obvious errors such as duplicate entries.
func (f *fsCPIO) mount(m MountPoint) error {
	for _, m := range f.mnts {
		if _, _, err := f.hasMount(m.n); err == nil {
			return fmt.Errorf("%q:%w", m.n, os.ErrExist)
		}
	}
	f.mnts = append(f.mnts, m)
	return nil
}

// ReadDir implements readdir for fsCPIO.
// If path is empty, ino 0 (root) is assumed.
func (fs *fsCPIO) ReadDir(filename string) ([]os.FileInfo, error) {
	verbose("fsCPIO readdir: %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.ReadDir(rel)
	}
	if s, err := fs.resolvelink(filename); err == nil {
		filename = s
	}
	verbose("fsCPIO readdir: %q", filename)
	l, err := fs.lookup(filename)
	if err != nil {
		return nil, err
	}
	fi, err := l.(*file).ReadDir(0, 1048576) // no idea what to do for size.
	if len(filename) == 0 {
		for _, m := range fs.mnts {
			// No clear union mount semantics on Linux
			// for "some but not all". Oh well.
			// Just continue
			mfi, err := m.fs.Lstat(".")
			verbose("mfi: %s %v %v", m.n, mfi, err)
			if err != nil {
				verbose("enumerating %q: %v", m.n, err)
				continue
			}
			fi = append(fi, &ufstat{FileInfo: mfi, name: m.n})
		}
	}
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

// Mode implements Mode for an fsCPIO.
func (f *fsCPIO) Mode() os.FileMode {
	m := uToGo(f.recs[0].Mode)
	verbose("fsCPIO mode: %v %#x", m, uint64(m))
	return m
}

// ModTime always returns 0.
func (f *fsCPIO) ModTime() time.Time {
	return time.Unix(0, 0)
}

// IsDir always returns true.
func (f *fsCPIO) IsDir() bool {
	verbose("fsCPIO mode: true")
	return true
}

// Sys returns nil, always.
func (f *fsCPIO) Sys() any {
	return nil
}

// Readlink implements ReadLink
func (fs *fsCPIO) Readlink(link string) (string, error) {
	if osfs, rel, err := fs.getfs(link); err == nil {
		return osfs.Readlink(rel)
	}
	l, err := fs.lookup(link)
	if err != nil {
		return "", err
	}
	return l.(*file).Readlink()
}

var _ billy.Filesystem = &fsCPIO{}

// file implements billy.Filly for fsCPIO files.
// A file is a server and an index into the cpio records,
// with some embedded structs to implement always-failing or
// always succeeding operations..
type file struct {
	no
	fileFail
	ok
	fs   *fsCPIO
	Path uint64
}

var _ billy.File = &file{}

// fstat implements fs.FileInfo.
type fstat struct {
	*cpio.Record
}

// Name implements Name.
func (f *fstat) Name() string {
	verbose("file Name(): rec %v", f.Record)
	return path.Base(f.Record.Name)
}

// Size implements Size.
func (f *fstat) Size() int64 {
	return int64(f.FileSize)
}

// Mode implements Mode.
func (f *fstat) Mode() os.FileMode {
	m := uToGo(f.Record.Mode)
	verbose("fstat mode: %v %#x", m, uint64(m))
	return m
}

// ModTime implements ModTime, always returning the Unix epoch.
func (f *fstat) ModTime() time.Time {
	return time.Unix(0, 0)
}

// IsDir implements IsDir.
func (f *fstat) IsDir() bool {
	verbose("fstat mode: %v", f.Mode()&cpio.S_IFDIR == cpio.S_IFDIR)
	return f.Mode().IsDir()
}

// Sys implements Sys, always returning nil.
func (f *fstat) Sys() any {
	return nil
}

// WithMount allows the addition of mounts to an fsCPIO,
// as part of a NewfsCPIO call.
func WithMount(n string, fs billy.Filesystem) MountPoint {
	return MountPoint{n: n, fs: fs}
}

// ufstat implements os.FileInfo, save that the name
// may be overridden. This is useful when the name of the
// FileInfo should be overridden, as in a MountPoint
type ufstat struct {
	os.FileInfo
	name string
}

// Name implements Name
func (u ufstat) Name() string {
	return u.name
}

// NewfsCPIO returns a fsCPIO, properly initialized.
func NewfsCPIO(c string, mounts ...MountPoint) (*fsCPIO, error) {
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

	fs := &fsCPIO{file: f, rr: rr, recs: recs, m: m}
	for _, m := range mounts {
		if err := fs.mount(m); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// resolvelink will try to follow the symlink to its resolution.
func (fs *fsCPIO) resolvelink(filename string) (string, error) {
	// Fun. For as long as readlink works,
	// and we've done less than (whatevs) 20 readlinks,
	// keep doing it. Then return what is left.
	var linkcount int
	var err error
	for {
		var s string
		if linkcount > 20 {
			return "", syscall.ELOOP
		}

		s, err = fs.Readlink(filename)
		// If we have walked it once, the first target was
		// a symlink. If it fails the first read, that is an
		// error.
		if linkcount > 0 && err == os.ErrInvalid {
			err = nil
			break
		}
		if err != nil {
			break
		}
		linkcount++
		if !path.IsAbs(s) {
			s = filepath.Join(filepath.Dir(filename), s)
		}
		filename = s
	}
	return filename, err
}

// Stat stats the file name.
// There's a little confusion here in billy and go-nfs.
// Unix kernels walk the file name component by component.
// stat on the client is responsible for handling walks of symlinks.
// It is critical to let the client do this, else it will be confused
// about actual pathnames. If we don't let the kernel do the work,
// we will have to do it here, and that way lies madness; we would have
// to reimplement the pathname-component by pathname-component walk..
func (fs *fsCPIO) Stat(filename string) (os.FileInfo, error) {
	verbose("fs: Stat %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		verbose("osfs stat %q", rel)
		m, err := osfs.Stat(rel)
		verbose("m %v err %v", m, err)
		return m, err
	}

	// Don't do this. The client does it.
	// filename, err := fs.resolvelink(filename)

	l, err := fs.lookup(filename)
	if err != nil {
		return nil, err
	}

	fi := &fstat{Record: &fs.recs[l.(*file).Path]}
	return fi, nil
}

// Lstat implements Lstat.
func (fs *fsCPIO) Lstat(filename string) (os.FileInfo, error) {
	verbose("fs: Lstat %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		verbose("osfs stat %q", rel)
		m, err := osfs.Lstat(rel)
		verbose("m %v err %v", m, err)
		return m, err
	}
	l, err := fs.lookup(filename)
	if err != nil {
		return nil, err
	}
	return &fstat{Record: &fs.recs[l.(*file).Path]}, nil
}

// rec returns a cpio.Record for a file.
func (l *file) rec() (*cpio.Record, error) {
	if int(l.Path) > len(l.fs.recs) {
		return nil, os.ErrNotExist
	}
	v("cpio:rec for %v is %v", l, l.fs.recs[l.Path])
	return &l.fs.recs[l.Path], nil
}

// getfs returns the filesystem, or error, for a given filename.
// It also returns the filename path relative to the filesystem mount.
func (fs *fsCPIO) getfs(filename string) (billy.Filesystem, string, error) {
	if l, rel, err := fs.hasMount(filename); err == nil {
		verbose("getfs: rel %q", rel)
		return l.fs, rel, nil
	}
	return nil, "", os.ErrNotExist
}

// lookup looks up a name in the fsCPIO. If the name is "",
// the root is assumed (this is what billy seems to require).
func (fs *fsCPIO) lookup(filename string) (billy.File, error) {
	var ino uint64
	if len(filename) > 0 {
		var ok bool
		ino, ok = fs.m[filename]
		verbose("lookup %q ino %d %v", filename, ino, ok)
		if !ok {
			return nil, os.ErrNotExist
		}
	}
	l := &file{Path: ino, fs: fs}
	return l, nil
}

// Join implements Join
func (fs *fsCPIO) Join(elem ...string) string {
	verbose("fs:Join(%q)", elem)
	n := path.Join(elem...)
	return n
}

// Open implements Open, searching, first, the mount points.
func (fs *fsCPIO) Open(filename string) (billy.File, error) {
	verbose("fs: Open %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.Open(rel)
	}
	return fs.lookup(filename)
}

// Create implements Create, searching, first, the mount points.
func (fs *fsCPIO) Create(filename string) (billy.File, error) {
	verbose("fs: Create %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.Create(rel)
	}
	return nil, os.ErrPermission
}

func (fs *fsCPIO) Rename(oldpath, newpath string) error {
	verbose("fs: Rename %q %q", oldpath, newpath)
	if oldosfs, oldrel, err := fs.getfs(oldpath); err == nil {
		newosfs, newrel, err := fs.getfs(newpath)
		if err != nil {
			return fmt.Errorf("Rename(%q,%q): %v", oldpath, newpath, err)
		}
		if newosfs != oldosfs {
			return fmt.Errorf("Rename(%q,%q): can not cross file systems", oldpath, newpath)
		}

		return newosfs.Rename(oldrel, newrel)
	}
	return os.ErrPermission
}

// MkdirAll implements billy.MkdirAll
func (fs *fsCPIO) MkdirAll(filename string, perm os.FileMode) error {
	verbose("fs: MkdirAll %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.MkdirAll(rel, perm)
	}
	return os.ErrPermission
}

// OpenFile implements OpenFile, searching, first, the mount points.
func (fs *fsCPIO) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	verbose("fs: OpenFile %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.OpenFile(rel, flag, perm)
	}
	return nil, os.ErrPermission
}

// Read implements nfs.ReadAt.
func (l *file) ReadAt(p []byte, offset int64) (int, error) {
	r, err := l.rec()
	if err != nil {
		return -1, err
	}
	return r.ReadAt(p, offset)
}

// Remove implements billy.Remove
func (fs *fsCPIO) Remove(filename string) error {
	verbose("fs: remove %q", filename)
	if osfs, rel, err := fs.getfs(filename); err == nil {
		return osfs.Remove(rel)
	}
	return os.ErrPermission
}

// Write implements nfs.WriteAt.
func (l *file) WriteAt(p []byte, offset int64) (int, error) {
	return -1, os.ErrPermission
}

// readdir returns a slice of indices for a directory, from
// the cpio records in the file system.
// See comment below as to why it must return a slice, not a range.
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
	r, err := l.rec()
	if err != nil {
		return "", err
	}
	if (&fstat{Record: r}).Mode().Type() != fs.ModeSymlink {
		return "", os.ErrInvalid
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

// srvNFS sets up an nfs server. dir string is for things like home.
// it might be dir ...string some day?
func srvNFS(cl *client.Cmd, n string, dir string) (func() error, string, error) {
	mdir, err := filepath.Rel("/", dir)
	if err != nil {
		return nil, "", err
	}
	osfs := NewOSFS(dir)
	verbose("Create New OSFS with %q", dir)
	mem, err := NewfsCPIO(n, WithMount(mdir, osfs))
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
	handler := NewNullAuthHandler(l, COS{mem}, u.String())
	log.Printf("uuid is %q", u.String())
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)
	f := func() error {
		return nfs.Serve(l, cacheHelper)
	}
	fstab := fmt.Sprintf("127.0.0.1:%s /tmp/cpu nfs rw,relatime,vers=3,rsize=1048576,wsize=1048576,namlen=255,hard,nolock,proto=tcp,port=%d,timeo=600,retrans=2,sec=sys,mountaddr=127.0.0.1,mountvers=3,mountport=%d,mountproto=tcp,local_lock=all,addr=127.0.0.1 0 0\n", u, portnfs, portnfs)
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
