// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"testing"

	memfs "github.com/go-git/go-billy/v5/memfs"
	"github.com/u-root/u-root/pkg/cpio"
)

func TestMemFS(t *testing.T) {
	f, err := os.Open("data/a.cpio")
	if err != nil {
		t.Fatal(err)
	}
	recReader := cpio.Newc.Reader(f)
	fs := memfs.New()
	err := ForEachRecord(recReader, func(r cpio.Record) error {
		var inums map[uint64]string
		inums = make(map[uint64]string)
		rr, err := archiver.NewFileReader(stdin)
		if err != nil {
			return err
		}
		for {
			rec, err := rr.ReadRecord()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("error reading records: %w", err)
			}
			debug("record name %s ino %d\n", rec.Name, rec.Ino)

			// A file with zero size could be a hard link to another file
			// in the archive. The file with content always comes first.
			//
			// But we should ignore files with Ino of 0; that's an illegal value.
			// The current most common use of this command is with u-root
			// initramfs cpio files on Linux and Harvey.
			// (nobody else cares about cpio any more save kernels).
			// Those always have Ino of zero for reproducible builds.
			// Hence doing the Ino != 0 test first saves a bit of work.
			if rec.Ino != 0 {
				switch rec.Mode & cpio.S_IFMT {
				// In any Unix past about V1, you can't do os.Link from user mode.
				// Except via mkdir of course :-).
				case cpio.S_IFDIR:
				default:
					// FileSize of non-zero means it is the first and possibly
					// only instance of this file.
					if rec.FileSize != 0 {
						break
					}
					// If the file is not in []inums it is a true zero-length file,
					// not a hard link to a file already seen.
					// (pedantic mode: on Unix all files are hard links;
					// so what this comment really means is "file with more than one
					// hard link).
					ino, ok := inums[rec.Ino]
					if !ok {
						break
					}
					err := os.Link(ino, rec.Name)
					debug("Hard linking %s to %s", ino, rec.Name)
					if err != nil {
						return err
					}
					continue
				}
				inums[rec.Ino] = rec.Name
			}
			debug("Creating file %s", rec.Name)
			if err := CreateFile(fs, rec); err != nil {
				log.Printf("Creating %q failed: %v", rec.Name, err)
			}
		}

	})
}
