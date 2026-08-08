//go:build !linux

package updater

import (
	"fmt"
	"net"
)

type OSPeerProvider struct{}

func (OSPeerProvider) Peer(net.Conn) (Peer, error) {
	return Peer{}, fmt.Errorf("SO_PEERCRED updater authentication is supported only on Linux")
}
