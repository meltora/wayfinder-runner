//go:build !linux

package updateipc

import (
	"fmt"
	"net"
)

func unixPeerUID(net.Conn) (uint32, error) {
	return 0, fmt.Errorf("root updater peer authentication is supported only on Linux")
}
