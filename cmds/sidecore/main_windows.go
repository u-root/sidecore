// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"

	"github.com/u-root/cpu/client"
)

func notify(c chan os.Signal) {

}

func sigerrors(c *client.Cmd, sig os.Signal) error {
	return nil
}
