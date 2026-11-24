package previewhost

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
)

const dockerHubRegistry = "registry-1.docker.io"

func registryHost(image string) string {
	repository, _, _ := strings.Cut(image, "@")
	first, _, nested := strings.Cut(repository, "/")
	if !nested || (!strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost") {
		return dockerHubRegistry
	}
	host := first
	if index := strings.LastIndex(host, ":"); index >= 0 {
		host = host[:index]
	}
	if host == "docker.io" || host == "index.docker.io" {
		return dockerHubRegistry
	}
	return host
}

func defaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func checkRegistryHost(ctx context.Context, lookup func(context.Context, string) ([]netip.Addr, error), host string) error {
	addresses, err := lookup(ctx, host)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		return errors.New("image registry host did not resolve")
	}
	for _, address := range addresses {
		if !publicAddress(address) {
			return errors.New("image registry host resolves to a refused address class")
		}
	}
	return nil
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() ||
		address.IsPrivate() || !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range deniedSpecialRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var deniedSpecialRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}
