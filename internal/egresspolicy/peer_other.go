//go:build !linux

package egresspolicy

import (
	"fmt"
	"net"
)

func unixPeerUID(net.Conn) (uint32, error) {
	return 0, fmt.Errorf("root egress verifier peer authentication is supported only on Linux")
}
