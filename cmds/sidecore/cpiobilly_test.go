// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
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
	if len(ents) != 2 {
		t.Fatalf(`Readdir("."): %d entries != 2 `, len(ents))
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

	fi, err = f.Stat("a/b/hosts")
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
func TestBillyFSMount(t *testing.T) {
	v = t.Logf
	osfs := NewOSFS("home")
	f, err := NewfsCPIO("data/a.cpio", WithMount("home", osfs))
	if err != nil {
		t.Fatalf("NewfsCPIO(\"data/a.cpio\", WithMount(\"data\", ...)): %v != nil", err)
	}

	// Make sure the underlying layers are there.
	fi, err := f.Stat("a/b/hosts")
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

}
