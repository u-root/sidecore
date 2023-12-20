// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
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
	t.Logf("root readdir, entries %v", ents)
	ents, err = f.ReadDir("a")
	if err != nil {
		t.Fatalf(`Readdir("a"): %v != nil `, err)
	}
	if len(ents) != 2 {
		t.Fatalf(`Readdir("a"): %d entries != 2 `, len(ents))
	}
	t.Logf("/a readdir, entries %v", ents)
	if ents[0].Name() != "." {
		t.Fatalf(`Readdir("a"): ents[0] name is %q, not '.'`, ents[0].Name())
	}
}
