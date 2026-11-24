package previewhost

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/example/app-runner/internal/netclass"
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
		if !netclass.Public(address) {
			return errors.New("image registry host resolves to a refused address class")
		}
	}
	return nil
}
