//go:build linux

package updateipc

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func unixPeerUID(conn net.Conn) (uint32, error) {
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return 0, fmt.Errorf("connection does not expose kernel credentials")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	if cred == nil {
		return 0, fmt.Errorf("kernel returned no peer credentials")
	}
	return cred.Uid, nil
}
