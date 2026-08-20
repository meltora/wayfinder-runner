package previewobserver

import (
	"context"
	"net"
	"net/netip"
	"time"
)

type Dependencies struct {
	LookupNetIP func(context.Context, string, string) ([]netip.Addr, error)
	DialContext func(context.Context, string, string) (net.Conn, error)
	Now         func() time.Time
}

func normalizeDependencies(input Dependencies) Dependencies {
	result := input
	if result.LookupNetIP == nil {
		resolver := net.DefaultResolver
		result.LookupNetIP = resolver.LookupNetIP
	}
	if result.DialContext == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}
		result.DialContext = dialer.DialContext
	}
	if result.Now == nil {
		result.Now = time.Now
	}
	return result
}
