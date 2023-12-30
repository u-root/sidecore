// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBillyFS(t *testing.T) {

	f, err := NewfsCPIO("data/a.cpio")
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\"): %v != nil", err)
	}

	fi, err := f.Stat(".")
	if err != nil {
		t.Fatalf(`Stat("."): %v != nil `, err)
	}
	if !fi.IsDir() {
		t.Fatalf(`Stat("."): IsDir() != true`)
	}
	ents, err := f.ReadDir(".")
	if err != nil {
		t.Fatalf(`Readdir("."): %v != nil `, err)
	}
	if len(ents) != 3 {
		t.Fatalf(`Readdir("."): %d entries != 3 `, len(ents))
	}

	h1, err := f.Open(".")
	if err != nil {
		t.Fatalf(`Open("."): %v != nil`, err)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("."): %v != nil`, err)
	}

	t.Logf("root readdir, entries %v", ents)
	ents, err = f.ReadDir("a")
	if err != nil {
		t.Fatalf(`Readdir("a"): %v != nil `, err)
	}
	if len(ents) != 1 {
		t.Fatalf(`Readdir("a"): %d entries != 1 `, len(ents))
	}
	t.Logf("/a readdir, entries %v", ents)
	if ents[0].Name() != "b" {
		t.Fatalf(`Readdir("a"): ents[0] name is %q, not 'b'`, ents[0].Name())
	}

	fi, err = f.Lstat("a/b/hosts")
	if err != nil {
		t.Fatalf(`Stat("a/b/hosts"): %v != nil `, err)
	}
	m := fi.Mode()
	if m.Type() != fs.ModeSymlink {
		t.Fatalf(`Stat("a/b/hosts").Mode(): %v != %v `, m.Type(), fs.ModeSymlink)
	}
	h1, err = f.Open("a/b/hosts")
	if err != nil {
		t.Fatalf(`Open("a/b/hosts"): %v != nil`, m.Type())
	}
	var b [512]byte
	if _, err := h1.ReadAt(b[:], 0); err == nil {
		t.Fatalf(`ReadAll("a/b/hosts"): nil != an error`)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("a/b/hosts"): %v != nil`, err)
	}

	l, err := f.Readlink("a/b/hosts")
	if err != nil {
		t.Fatalf(`Readlink("a/b/hosts"): %v != nil `, err)
	}
	if l != "c/d/hosts" {
		t.Fatalf(`Readlink("a/b/hosts"): %s != "c/d/hosts"`, l)
	}

	h1, err = f.Open("a/b/c/d/hosts")
	if err != nil {
		t.Fatalf(`Open("a/b/c/d/hosts"): %v != nil`, m.Type())
	}
	n, err := h1.ReadAt(b[:], 0)
	// ReadAt is allowed to return io.EOF OR nil when
	// n is < len(b)
	//     When ReadAt returns n < len(p), it returns a non-nil error explaining why
	//     more bytes were not returned. In this respect, ReadAt is stricter than Read.

	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf(`ReadAll("a/b/c/d/hosts"): %v != nil`, err)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("a/b/c/d/hosts"): %v != nil`, err)
	}

	hosts, err := os.ReadFile("data/a/b/c/d/hosts")
	if err != nil {
		t.Fatalf(`reading "data/a/b/c/d/hosts": %v != nil`, err)
	}
	if string(hosts) != string(b[:n]) {
		t.Fatalf(`reading "data/a/b/c/d/hosts": %q != %q`, string(b[:n]), string(hosts))
	}

}

func TestBillySymlink(t *testing.T) {
	f, err := NewfsCPIO("data/a.cpio")
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\"): %v != nil", err)
	}

	fi, err := f.Lstat("a/b/hosts")
	if err != nil {
		t.Fatalf(`Stat("a/b/hosts"): %v != nil `, err)
	}
	m := fi.Mode()
	if m.Type() != fs.ModeSymlink {
		t.Fatalf(`Stat("a/b/hosts").Mode(): %v != %v `, m.Type(), fs.ModeSymlink)
	}
	h1, err := f.Open("a/b/hosts")
	if err != nil {
		t.Fatalf(`Open("a/b/hosts"): %v != nil`, m.Type())
	}
	var b [512]byte
	if _, err := h1.ReadAt(b[:], 0); err == nil {
		t.Fatalf(`ReadAll("a/b/hosts"): nil != an error`)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("a/b/hosts"): %v != nil`, err)
	}

	if _, err = f.Readlink("a/b/22"); err == nil {
		t.Fatalf(`Readlink("a/b/22"): nil != %v`, syscall.ELOOP)
	}

	if _, err = f.Readlink("a/b/c/d/hosts"); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf(`Readlink("a/b/22"): nil != %v`, os.ErrInvalid)
	}
}

func TestBillyFSMount(t *testing.T) {
	v = t.Logf
	osfs := NewOSFS("home")
	f, err := NewfsCPIO("data/a.cpio", WithMount("home", osfs))
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\", WithMount(\"data\", ...)): %v != nil", err)
	}

	// Make sure the underlying layers are there.
	fi, err := f.Lstat("a/b/hosts")
	if err != nil {
		t.Fatalf(`Stat("a/b/hosts"): %v != nil `, err)
	}
	m := fi.Mode()
	if m.Type() != fs.ModeSymlink {
		t.Fatalf(`Stat("a/b/hosts").Mode(): %v != %v `, m.Type(), fs.ModeSymlink)
	}

	// Yes, Stat does not follow symlinks. The remote nfs client,
	// the kernel, expects that.
	fi, err = f.Stat("a/b/hosts")
	if err != nil {
		t.Fatalf(`Stat("a/b/hosts"): %v != nil `, err)
	}
	m = fi.Mode()
	if m.Type() != fs.ModeSymlink {
		t.Fatalf(`Stat("a/b/hosts").Mode(): %v != true `, m.Type() != fs.ModeSymlink)
	}
	h1, err := f.Open("a/b/hosts")
	if err != nil {
		t.Fatalf(`Open("a/b/hosts"): %v != nil`, m.Type())
	}
	var b [512]byte
	if _, err := h1.ReadAt(b[:], 0); err == nil {
		t.Fatalf(`ReadAll("a/b/hosts"): nil != an error`)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("a/b/hosts"): %v != nil`, err)
	}

	l, err := f.Readlink("a/b/hosts")
	if err != nil {
		t.Fatalf(`Readlink("a/b/hosts"): %v != nil `, err)
	}
	if l != "c/d/hosts" {
		t.Fatalf(`Readlink("a/b/hosts"): %s != "c/d/hosts"`, l)
	}

	h1, err = f.Open("a/b/c/d/hosts")
	if err != nil {
		t.Fatalf(`Open("a/b/c/d/hosts"): %v != nil`, err)
	}
	n, err := h1.ReadAt(b[:], 0)
	// ReadAt is allowed to return io.EOF OR nil when
	// n is < len(b)
	//     When ReadAt returns n < len(p), it returns a non-nil error explaining why
	//     more bytes were not returned. In this respect, ReadAt is stricter than Read.

	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf(`ReadAll("a/b/c/d/hosts"): %v != nil`, err)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("a/b/c/d/hosts"): %v != nil`, err)
	}

	hosts, err := os.ReadFile("data/a/b/c/d/hosts")
	if err != nil {
		t.Fatalf(`reading "data/a/b/c/d/hosts": %v != nil`, err)
	}
	if string(hosts) != string(b[:n]) {
		t.Fatalf(`reading "data/a/b/c/d/hosts": %q != %q`, string(b[:n]), string(hosts))
	}

	// Now see if the mounted-on bits work.
	h1, err = f.Open("home/glenda/hosts")
	if err != nil {
		t.Fatalf(`Open("home/glenda/hosts"): %v != nil`, err)
	}
	n, err = h1.ReadAt(b[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf(`ReadAll("a/b/c/d/hosts"): %v != nil`, err)
	}

	if err := h1.Close(); err != nil {
		t.Fatalf(`Close("home/glenda/hosts"): %v != nil`, err)
	}

	if string(hosts) != string(b[:n]) {
		t.Fatalf(`reading "home/glenda/hosts": %q != %q`, string(b[:n]), string(hosts))
	}

	l, err = f.Readlink("home/glenda/h")
	if err != nil {
		t.Fatalf(`Readlink("home/glenda/h"): %v != nil `, err)
	}
	if l != "hosts" {
		t.Fatalf(`Readlink("home/glenda/h"): %s != "hosts"`, l)
	}

	ents, err := f.ReadDir("home")
	if err != nil {
		t.Fatalf(`Readdir("home"): %v != nil `, err)
	}
	osents, err := os.ReadDir("home")
	if err != nil {
		t.Fatalf(`os.Readdir("home"): %v != nil `, err)
	}
	t.Logf("Readdir home: %q, %v", ents, err)
	if len(ents) != len(osents) {
		t.Fatalf(`Readdir("home"): %d entries != %d`, len(ents), len(osents))
	}

	fi, err = f.Stat("home/glenda/h")
	if err != nil {
		t.Fatalf(`Stat("home/glenda/h"): %v != nil `, err)
	}
	fi, err = f.Lstat("home/glenda/h")
	if err != nil {
		t.Fatalf(`Stat("home/glenda/h"): %v != nil `, err)
	}
	m = fi.Mode()
	if m.Type() != fs.ModeSymlink {
		t.Fatalf(`Stat("home/glenda/h").Mode(): %v != %v `, m.Type(), fs.ModeSymlink)
	}
}

func TestBillySymlinkLib(t *testing.T) {
	f, err := NewfsCPIO("data/a.cpio")
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\"): %v != nil", err)
	}

	// This is not how the kernel walks files; it uses the
	// classic ntoi() loop to walk one pathname component at
	// a time. So simulate that.
	//h1, err := f.Open("lib/b/hosts")
	ents, err := f.ReadDir("lib")
	if err != nil {
		t.Fatalf(`Readdir("lib"): %v != nil `, err)
	}
	if len(ents) != 1 {
		t.Fatalf(`Readdir("lib"): %d entries != 1 `, len(ents))
	}
	t.Logf("lib readdir, entries %v", ents)
	if ents[0].Name() != "b" {
		t.Fatalf(`Readdir("lib"): ents[0] name is %q, not 'b'`, ents[0].Name())
	}
}

func TestBillyFSRename(t *testing.T) {
	dir := t.TempDir()
	v = t.Logf
	osfs := NewOSFS(dir)
	rdir, err := filepath.Rel("/", dir)
	t.Logf("dir %q rdir %q", dir, rdir)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := NewfsCPIO("data/a.cpio", WithMount(rdir, osfs))
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\", WithMount(%q, ...)): %v != nil", dir, err)
	}

	oldn := filepath.Join(dir, "a")
	if err := os.WriteFile(oldn, []byte{}, 0666); err != nil {
		t.Fatal(err)
	}

	oldn = filepath.Join(rdir, "a")
	newn := filepath.Join(rdir, "b")
	if err := fs.Rename(oldn, newn); err != nil {
		t.Errorf("Rename %q to %q: %v != nil", oldn, newn, err)
	}
	if err := fs.Rename(newn, oldn); err != nil {
		t.Errorf("Rename %q to %q: %v != nil", oldn, newn, err)
	}
	if err := fs.Rename("a/b/c/d/hosts", "a/b/c/d/hosts2"); err == nil {
		t.Errorf("Rename %q to %q: nil != an errro", oldn, newn)
	}
	if err := fs.Rename("a/b/c/d/hosts", oldn); err == nil {
		t.Errorf("Rename %q to %q: nil != an errro", oldn, newn)
	}
	if err := fs.Rename(oldn, "a/b/c/d/hosts"); err == nil {
		t.Errorf("Rename %q to %q: nil != an errro", oldn, newn)
	}
}

func TestBillyFSMkdirAll(t *testing.T) {
	dir := t.TempDir()
	v = t.Logf
	osfs := NewOSFS(dir)
	rdir, err := filepath.Rel("/", dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := NewfsCPIO("data/a.cpio", WithMount(rdir, osfs))
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\", WithMount(%q, ...)): %v != nil", dir, err)
	}

	n := filepath.Join(rdir, "a/b/c/d/e")
	if err := fs.MkdirAll(n, 0777); err != nil {
		t.Errorf("MkdirAll %q: %v != nil", n, err)
	}

	real := filepath.Join(dir, "a/b/c/d/e")
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("Checking first result:%v", err)
	}

	if err := fs.MkdirAll("a/b", 0777); err == nil {
		t.Errorf("MkdirAll \"a/b\": nil != an error")
	}
}

func TestBillyFSSymlink(t *testing.T) {
	dir := t.TempDir()
	v = t.Logf
	osfs := NewOSFS(dir)
	rdir, err := filepath.Rel("/", dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := NewfsCPIO("data/a.cpio", WithMount(rdir, osfs))
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\", WithMount(%q, ...)): %v != nil", dir, err)
	}

	n := filepath.Join(rdir, "a")
	if err := fs.Symlink("value", n); err != nil {
		t.Errorf("Symlink %q -> \"value\": %v != nil", n, err)
	}

	r := filepath.Join(dir, "a")
	if v, err := os.Readlink(r); err != nil || v != "value" {
		t.Errorf("Readlink(%s): %s, %v != \"value\", nil", r, v, err)
		dents, err := os.ReadDir(dir)
		t.Logf("contents of %q: %v, %v", dir, dents, err)
	}

	if err := fs.Symlink("a/z", "value"); err == nil {
		t.Errorf("Symlink \"a/b\" -> \"value\": nil != an error")
	}
}
