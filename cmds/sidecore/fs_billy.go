// Copyright 2013-2017 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/u-root/u-root/pkg/cpio"
	"github.com/u-root/u-root/pkg/uio"
	"github.com/u-root/u-root/pkg/upath"
	"golang.org/x/sys/unix"

	"github.com/go-git/go-billy/v5"
)

// Unix mode_t bits.
const (
	modeTypeMask    = 0o170000
	modeSocket      = 0o140000
	modeSymlink     = 0o120000
	modeFile        = 0o100000
	modeBlock       = 0o060000
	modeDir         = 0o040000
	modeChar        = 0o020000
	modeFIFO        = 0o010000
	modeSUID        = 0o004000
	modeSGID        = 0o002000
	modeSticky      = 0o001000
	modePermissions = 0o000777
)

var modeMap = map[uint64]os.FileMode{
	modeSocket:  os.ModeSocket,
	modeSymlink: os.ModeSymlink,
	modeFile:    0,
	modeBlock:   os.ModeDevice,
	modeDir:     os.ModeDir,
	modeChar:    os.ModeCharDevice,
	modeFIFO:    os.ModeNamedPipe,
}

// setModes sets the modes, changing the easy ones first and the harder ones last.
// In this way, we set as much as we can before bailing out.
// N.B.: if you set something with S_ISUID, then change the owner,
// the kernel (Linux, OSX, etc.) clears S_ISUID (a good idea). So, the simple thing:
// Do the chmod operations in order of difficulty, and give up as soon as we fail.
// Set the basic permissions -- not including SUID, GUID, etc.
// Set the times
// Set the owner
// Set ALL the mode bits, in case we need to do SUID, etc. If we could not
// set the owner, we won't even try this operation of course, so we won't
// have SUID incorrectly set for the wrong user.
func setModes(fs billy.Filesystem, r cpio.Record) error {
	if err := fs.Chmod(r.Name, toFileMode(r)&os.ModePerm); err != nil {
		return err
	}
	if err := fs.Chown(r.Name, int(r.UID), int(r.GID)); err != nil {
		return err
	}
	if err := fs.Chmod(r.Name, toFileMode(r)); err != nil {
		return err
	}
	return nil
}

func toFileMode(fs billy.FileSytem, r cpio.Record) os.FileMode {
	m := fs.FileMode(perm(r))
	if r.Mode&unix.S_ISUID != 0 {
		m |= os.ModeSetuid
	}
	if r.Mode&unix.S_ISGID != 0 {
		m |= os.ModeSetgid
	}
	if r.Mode&unix.S_ISVTX != 0 {
		m |= os.ModeSticky
	}
	return m
}

func perm(r cpio.Record) uint32 {
	return uint32(r.Mode) & modePermissions
}

func dev(r cpio.Record) int {
	return int(r.Rmajor<<8 | r.Rminor)
}

func linuxModeToFileType(m uint64) (os.FileMode, error) {
	if t, ok := modeMap[m&modeTypeMask]; ok {
		return t, nil
	}
	return 0, fmt.Errorf("invalid file type %#o", m&modeTypeMask)
}

// CreateFile creates a local file for f relative to the current working
// directory.
//
// CreateFile will attempt to set all metadata for the file, including
// ownership, times, and permissions.
func CreateFile(fs billy.Filesystem, f cpio.Record) error {
	return CreateFileInRoot(fs, f, ".", true)
}

// CreateFileInRoot creates a local file for f relative to rootDir.
//
// It will attempt to set all metadata for the file, including ownership,
// times, and permissions. If these fail, it only returns an error if
// forcePriv is true.
//
// Block and char device creation will only return error if forcePriv is true.
func CreateFileInRoot(fs billy.Filesystem, f cpio.Record, rootDir string, forcePriv bool) error {
	m, err := linuxModeToFileType(f.Mode)
	if err != nil {
		return err
	}

	f.Name, err = upath.SafeFilepathJoin(rootDir, f.Name)
	if err != nil {
		// The behavior is to skip files which are unsafe due to
		// zipslip, but continue extracting everything else.
		log.Printf("Warning: Skipping file %q due to: %v", f.Name, err)
		return nil
	}
	dir := filepath.Dir(f.Name)
	// The problem: many cpio archives do not specify the directories and
	// hence the permissions. They just specify the whole path.  In order
	// to create files in these directories, we have to make them at least
	// mode 755.
	if _, err := fs.Stat(dir); os.IsNotExist(err) && len(dir) > 0 {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("CreateFileInRoot %q: %v", f.Name, err)
		}
	}

	switch m {
	case os.ModeSocket, os.ModeNamedPipe:
		return fmt.Errorf("%q: type %v: cannot create IPC endpoints", f.Name, m)

	case os.ModeSymlink:
		content, err := io.ReadAll(uio.Reader(f))
		if err != nil {
			return err
		}
		return fs.Symlink(string(content), f.Name)

	case os.FileMode(0):
		nf, err := fs.Create(f.Name)
		if err != nil {
			return err
		}
		defer nf.Close()
		if _, err := io.Copy(nf, uio.Reader(f)); err != nil {
			return err
		}

	case os.ModeDir:
		if err := fs.MkdirAll(f.Name, toFileMode(f)); err != nil {
			return err
		}

	case os.ModeDevice:
		if err := mknod(fs, f.Name, perm(f)|syscall.S_IFBLK, dev(f)); err != nil && forcePriv {
			return err
		}

	case os.ModeCharDevice:
		if err := mknod(fs, f.Name, perm(f)|syscall.S_IFCHR, dev(f)); err != nil && forcePriv {
			return err
		}

	default:
		return fmt.Errorf("%v: Unknown type %#o", f.Name, m)
	}

	if err := setModes(fs, f); err != nil && forcePriv {
		return err
	}
	return nil
}

// Inumber and devnumbers are unique to Unix-like
// operating systems. You can not uniquely disambiguate a file in a
// Unix system with just an inumber, you need a device number too.
// To handle hard links (unique to Unix) we need to figure out if a
// given file has been seen before. To do this we see if a file has the
// same [dev,ino] tuple as one we have seen. If so, we won't bother
// reading it in.

type devInode struct {
	dev uint64
	ino uint64
}
