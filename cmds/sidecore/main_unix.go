// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows && !plan9

package main

import (
	"os"
	"os/signal"

	"github.com/u-root/cpu/client"
	ossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func notify(c chan os.Signal) {
	signal.Notify(c, unix.SIGINT, unix.SIGTERM)
}

func sigerrors(c *client.Cmd, sig os.Signal) error {
	var sigErr error
	switch sig {
	case unix.SIGINT:
		sigErr = c.Signal(ossh.SIGINT)
	case unix.SIGTERM:
		sigErr = c.Signal(ossh.SIGTERM)
	}
	return sigErr
}
