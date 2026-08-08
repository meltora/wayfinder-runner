//go:build linux

package updater

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

type OSPeerProvider struct{}

func (OSPeerProvider) Peer(conn net.Conn) (Peer, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("connection is not Unix")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) { cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return Peer{}, err
	}
	if sockErr != nil {
		return Peer{}, sockErr
	}
	if cred == nil || cred.Pid <= 0 {
		return Peer{}, fmt.Errorf("missing peer credentials")
	}
	return Peer{UID: int(cred.Uid), GID: int(cred.Gid), PID: int(cred.Pid)}, nil
}
