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
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/u-root/u-root/pkg/cpio"
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
func (*no) Open(filename string) (billy.File, error)   { return nil, os.ErrInvalid }
func (*no) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	return nil, os.ErrInvalid
}
func (*no) Stat(filename string) (os.FileInfo, error) { return nil, os.ErrInvalid }
func (*no) Rename(oldpath, newpath string) error      { return os.ErrInvalid }
func (*no) Remove(filename string) error              { return os.ErrInvalid }
func (*no) Join(elem ...string) string                { return path.Join(elem...) }

// TempFile
func (*no) TempFile(dir, prefix string) (billy.File, error) { return nil, os.ErrPermission }

// Dir
func (*no) ReadDir(path string) ([]os.FileInfo, error)       { panic("readdir"); return nil, os.ErrInvalid }
func (*no) MkdirAll(filename string, perm os.FileMode) error { return os.ErrPermission }

// Symlink
func (*no) Lstat(filename string) (os.FileInfo, error) { panic("Lstat"); return nil, os.ErrInvalid }
func (*no) Symlink(target, link string) error          { return os.ErrPermission }
func (*no) Readlink(link string) (string, error)       { panic("readlink"); return "", os.ErrPermission }

type fsCPIO struct {
	no
	file *os.File
	rr   cpio.RecordReader
	m    map[string]uint64
	recs []cpio.Record
}

var _ billy.Filesystem = &fsCPIO{}

// A file is a server and an index into the cpio records.
type file struct {
	fs   *fsCPIO
	path uint64
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

func (l *file) rec() (*cpio.Record, error) {
	if int(l.path) > len(l.fs.recs) {
		return nil, os.ErrNotExist
	}
	v("cpio:rec for %v is %v", l, l.fs.recs[l.path])
	return &l.fs.recs[l.path], nil
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

func (l *file) readdir() ([]uint64, error) {
	verbose("cpio:readdir at %d", l.path)
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
	for i, r := range l.fs.recs[l.path+1:] {
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
		list = append(list, uint64(i)+l.path+1)
	}
	return list, nil
}
/*
// Readdir implements p9.File.Readdir.
// This is a bit of a mess in cpio, but the good news is that
// files will be in some sort of order ...
func (l *file) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
/*
	list, err := l.readdir()
	if err != nil {
		return nil, err
	}
	if offset > uint64(len(list)) {
		return nil, io.EOF
	}
	verbose("cpio:readdir list %v", list)
	var dirents p9.Dirents
	dirents = append(dirents, p9.Dirent{
		QID:    qid,
		Type:   qid.Type,
		Name:   ".",
		Offset: l.path,
	})
	verbose("cpio:add path %d '.'", l.path)
	//log.Printf("cpio:readdir %q returns %d entries start at offset %d", l.path, len(fi), offset)
	for _, i := range list[offset:] {
		entry := file{path: i, fs: l.fs}
		qid, _, err := entry.info()
		if err != nil {
			continue
		}
		r, err := entry.rec()
		if err != nil {
			continue
		}
		verbose("cpio:add path %d %q", i, filepath.Base(r.Info.Name))
		dirents = append(dirents, p9.Dirent{
			QID:    qid,
			Type:   qid.Type,
			Name:   filepath.Base(r.Info.Name),
			Offset: i,
		})
	}

	verbose("cpio:readdir:return %v, nil", dirents)
	return dirents, nil

}
*/
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

/*
// GetAttr implements p9.File.GetAttr.
//
// Not fully implemented.
func (l *file) GetAttr(req p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	qid, fi, err := l.info()
	if err != nil {
		return qid, p9.AttrMask{}, p9.Attr{}, err
	}

	//you are not getting symlink!
	attr := p9.Attr{
		Mode:             p9.FileMode(fi.Mode),
		UID:              p9.UID(fi.UID),
		GID:              p9.GID(fi.GID),
		NLink:            p9.NLink(fi.NLink),
		RDev:             p9.Dev(fi.Dev),
		Size:             uint64(fi.FileSize),
		BlockSize:        uint64(4096),
		Blocks:           uint64(fi.FileSize / 4096),
		ATimeSeconds:     uint64(0),
		ATimeNanoSeconds: uint64(0),
		MTimeSeconds:     uint64(fi.MTime),
		MTimeNanoSeconds: uint64(0),
		CTimeSeconds:     0,
		CTimeNanoSeconds: 0,
	}
}
*/