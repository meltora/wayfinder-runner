package main

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSocketActivationAcceptsExactlyOneUnixListener(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "updater-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	raw, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	file, err := raw.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	env := map[string]string{"LISTEN_PID": strconv.Itoa(os.Getpid()), "LISTEN_FDS": "1"}
	ln, active, err := activationListener(func(k string) string { return env[k] }, os.Getpid(), func(fd uintptr, name string) *os.File {
		if fd != 3 {
			t.Fatalf("fd=%d", fd)
		}
		dup, err := fileDup(file)
		if err != nil {
			t.Fatal(err)
		}
		return dup
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !active {
		t.Fatal("socket activation not detected")
	}
	if _, ok := ln.(*net.UnixListener); !ok {
		t.Fatalf("listener=%T", ln)
	}
}
func TestSocketActivationFailsClosedOnMalformedEnvironment(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{{"LISTEN_PID": "1"}, {"LISTEN_FDS": "2", "LISTEN_PID": strconv.Itoa(os.Getpid())}, {"LISTEN_FDS": "1", "LISTEN_PID": "bad"}}
	for _, env := range cases {
		if _, _, err := activationListener(func(k string) string { return env[k] }, os.Getpid(), func(uintptr, string) *os.File { return nil }); err == nil {
			t.Fatalf("accepted env=%v", env)
		}
	}
}
func fileDup(f *os.File) (*os.File, error) {
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}
