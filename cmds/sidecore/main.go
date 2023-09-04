// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	// We use this ssh because it implements port redirection.
	// It can not, however, unpack password-protected keys yet.

	"github.com/hugelgupf/p9/p9"
	config "github.com/kevinburke/ssh_config"
	"github.com/u-root/cpu/client"
	"github.com/u-root/cpu/ds"
	"github.com/u-root/u-root/pkg/ulog"

	// We use this ssh because it can unpack password-protected private keys.
	ossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const defaultPort = "17010"

type cpu struct {
	host      string
	port      string
	keyfile   string
	hostkey   string
	namespace string
}

var (
	defaultKeyFile = filepath.Join(os.Getenv("HOME"), ".ssh/cpu_rsa")
	// For the ssh server part
	debug = flag.Bool("d", false, "enable debug prints")
	dbg9p = flag.Bool("dbg9p", false, "show 9p io")
	dump  = flag.Bool("dump", false, "Dump copious output, including a 9p trace, to a temp file at exit")
	// for now, don't allow this. It will get too confusing. fstab       = flag.String("fstab", "", "pass an fstab to the cpud")
	hostKeyFile = flag.String("hk", "" /*"/etc/ssh/ssh_host_rsa_key"*/, "file for host key")
	keyFile     = flag.String("key", "", "key file")
	network     = flag.String("net", "", "network type to use. Defaults to whatever the cpu client defaults to")
	port        = flag.String("sp", "", "cpu default port")
	root        = flag.String("root", "/", "9p root")
	timeout9P   = flag.String("timeout9p", "100ms", "time to wait for the 9p mount to happen.")
	ninep       = flag.Bool("9p", true, "Enable the 9p mount in the client")

	// v allows debug printing.
	// Do not call it directly, call verbose instead.
	v          = func(string, ...interface{}) {}
	dumpWriter *os.File
)

// These variables are in addition to the regular CPU command, for ds support.
var (
	container = flag.String("container", "riscv-ubuntu@latest.cpio", "flattened docker container file")
	numCPUs   = flag.Int("n", 1, "number CPUs to run on")
)

func verbose(f string, a ...interface{}) {
	v("CPU:"+f+"\r\n", a...)
}

func envOrDefault(name, defaultName string) string {
	if n, ok := os.LookupEnv(name); ok {
		return n
	}
	return defaultName
}

func flags() ([]cpu, []string, error) {
	flag.Parse()
	if *dump && *debug {
		return nil, nil, fmt.Errorf("You can only set either dump OR debug")
	}
	if *debug {
		v = log.Printf
		client.SetVerbose(verbose)
	}
	if *dump {
		var err error
		dumpWriter, err = ioutil.TempFile("", "cpu")
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Logging to %s", dumpWriter.Name())
		*dbg9p = true
		ulog.Log = log.New(dumpWriter, "", log.Ltime|log.Lmicroseconds)
		v = ulog.Log.Printf
	}
	args := flag.Args()
	host := ds.DsDefault
	arch := envOrDefault("SIDECOREARCH", runtime.GOARCH)

	a := []string{}
	if len(args) > 0 {
		host = args[0]
		a = args[1:]
	}
	if host == "." {
		host = fmt.Sprintf("%s&arch=%s", ds.DsDefault, arch)
		v("host specification is %q", host)
	}

	if len(a) == 0 {
		if *numCPUs > 1 {
			log.Fatal("Interactive access with more than one CPU is not supported (yet)")
		}
		shellEnv := os.Getenv("SHELL")
		if len(shellEnv) > 0 {
			a = []string{shellEnv}
		} else {
			a = []string{"/bin/sh"}
		}
	}

	// Try to parse it as a dnssd: path.
	// If that fails, we will run as though
	// it were just a host name.
	dq, err := ds.Parse(host)

	var cpus []cpu

	if err == nil {
		c, err := ds.Lookup(dq, *numCPUs)
		if err != nil {
			log.Printf("%v", err)
		}
		for _, e := range c {
			cpus = append(cpus, cpu{host: e.Entry.IPs[0].String(), port: strconv.Itoa(e.Entry.Port)})
		}
	} else {
		cpus = append(cpus, cpu{host: host, port: *port})
	}

	return cpus, a, nil

}

// getKeyFile picks a keyfile if none has been set.
// It will use sshconfig, else use a default.
func getKeyFile(host, kf string) string {
	verbose("getKeyFile for %q", kf)
	if len(kf) == 0 {
		kf = config.Get(host, "IdentityFile")
		verbose("key file from config is %q", kf)
		if len(kf) == 0 {
			kf = defaultKeyFile
		}
	}
	// The kf will always be non-zero at this point.
	if strings.HasPrefix(kf, "~") {
		kf = filepath.Join(os.Getenv("HOME"), kf[1:])
	}
	verbose("getKeyFile returns %q", kf)
	// this is a tad annoying, but the config package doesn't handle ~.
	return kf
}

// getHostName reads the host name from the config file,
// if needed. If it is not found, the host name is returned.
func getHostName(host string) string {
	h := config.Get(host, "HostName")
	if len(h) != 0 {
		host = h
	}
	return host
}

// getPort gets a port.
// The rules here are messy, since config.Get will return "22" if
// there is no entry in .ssh/config. 22 is not allowed. So in the case
// of "22", convert to defaultPort
func getPort(host, port string) string {
	p := port
	verbose("getPort(%q, %q)", host, port)
	if len(port) == 0 {
		if cp := config.Get(host, "Port"); len(cp) != 0 {
			verbose("config.Get(%q,%q): %q", host, port, cp)
			p = cp
		}
	}
	if len(p) == 0 || p == "22" {
		p = defaultPort
		verbose("getPort: return default %q", p)
	}
	verbose("returns %q", p)
	return p
}

func newCPU(srv p9.Attacher, cpu *cpu, args ...string) (retErr error) {
	// note that 9P is enabled if namespace is not empty OR if ninep is true
	c := client.Command(cpu.host, args...)
	defer func() {
		verbose("close")
		if err := c.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("Close: %v", err)
		}
		verbose("close done")
	}()

	c.Env = os.Environ()

	client.Debug9p = *dbg9p

	if err := c.SetOptions(
		client.WithPrivateKeyFile(cpu.keyfile),
		client.WithHostKeyFile(cpu.hostkey),
		client.WithPort(cpu.port),
		client.WithRoot(*root),
		client.WithNameSpace(cpu.namespace),
		client.With9P(*ninep),
		client.WithNetwork(*network),
		client.WithServer(srv),
		client.WithTimeout(*timeout9P)); err != nil {
		log.Fatal(err)
	}
	if err := c.Dial(); err != nil {
		return fmt.Errorf("Dial: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	defer close(sigChan)
	signal.Notify(sigChan, unix.SIGINT, unix.SIGTERM)
	defer signal.Stop(sigChan)
	errChan := make(chan error, 1)
	defer close(errChan)

	go func() {
		verbose("start")
		if err := c.Start(); err != nil {
			errChan <- fmt.Errorf("Start: %v", err)
			return
		}
		verbose("wait")
		errChan <- c.Wait()
	}()

	var err error
loop:
	for {
		select {
		case sig := <-sigChan:
			var sigErr error
			switch sig {
			case unix.SIGINT:
				sigErr = c.Signal(ossh.SIGINT)
			case unix.SIGTERM:
				sigErr = c.Signal(ossh.SIGTERM)
			}
			if sigErr != nil {
				verbose("sending %v to %q: %v", sig, c.Args[0], sigErr)
			} else {
				verbose("signal %v sent to %q", sig, c.Args[0])
			}
		case err = <-errChan:
			break loop
		}
	}

	return err
}

func usage(err error) {
	var b bytes.Buffer
	flag.CommandLine.SetOutput(&b)
	flag.PrintDefaults()
	log.Fatalf("%v:Usage: cpu [options] host [shell command]:\n%v", err, b.String())
}

func main() {
	home := filepath.Dir(os.Getenv("HOME"))
	h, err := filepath.Rel("/", home)
	if err != nil {
		h = "home"
	}

	var namespace = flag.String("namespace", "/lib:/lib64:/usr:/bin:/etc:"+home, "Default namespace for the remote process -- set to none for none")
	cpus, args, err := flags()
	if err != nil {
		usage(err)
	}
	// The remote system, for now, is always Linux or a standard Unix (or Plan 9)
	// It will never be darwin (go argue with Apple)
	// so /tmp is *always* /tmp
	if err := os.Setenv("TMPDIR", "/tmp"); err != nil {
		log.Printf("Warning: could not set TMPDIR: %v", err)
	}

	if !filepath.IsAbs(*container) {
		// Find the flattened container to use
		cdir, ok := os.LookupEnv("SIDECORE_IMAGES")
		if !ok {
			cdir = filepath.Join(os.Getenv("HOME"), "sidecore-images")
		}
		*container = filepath.Join(cdir, *container)
	}
	if _, err := os.Stat(*container); err != nil {
		log.Fatalf("Can not open container: %v", err)
	}

	// create 9p servers for the cpio and /.
	cpioserv, err := client.NewCPIO9P(*container)
	if err != nil {
		log.Fatal(err)
	}
	cpiofs, err := cpioserv.Attach()
	if err != nil {
		log.Fatal(err)
	}

	// NewCPU9P returns a CPU9P, properly initialized.
	fssrv := client.NewCPU9P("/")
	fs, err := fssrv.Attach()
	if err != nil {
		log.Fatal(err)
	}
	verbose("fs %v", fs)

	u, err := client.NewUnion9P([]client.UnionMount{
		client.NewUnionMount([]string{h}, fs),
		client.NewUnionMount([]string{}, cpiofs),
	})
	verbose("u is %v", u)
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, cpu := range cpus {
		wg.Add(1)
		cpu.keyfile = getKeyFile(cpu.host, *keyFile)
		cpu.port = getPort(cpu.host, cpu.port)
		cpu.host = getHostName(cpu.host)
		cpu.hostkey = *hostKeyFile
		cpu.namespace = *namespace

		verbose("cpu to %v:%v", cpu.host, cpu.port)
		if err := newCPU(u, &cpu, args...); err != nil {
			e := 1
			log.Printf("SSH error %s", err)
			sshErr := &ossh.ExitError{}
			if errors.As(err, &sshErr) {
				e = sshErr.ExitStatus()
			}
			log.Printf("%v", e)
		}
		wg.Done()
	}
	wg.Wait()
}
