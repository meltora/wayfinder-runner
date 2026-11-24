package previewhost

import (
	"context"
	"net/netip"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	for image, want := range map[string]string{
		"node@sha256:abc":                        "registry-1.docker.io",
		"library/node@sha256:abc":                "registry-1.docker.io",
		"docker.io/library/node@sha256:abc":      "registry-1.docker.io",
		"ghcr.io/org/app@sha256:abc":             "ghcr.io",
		"registry.example.com:5000/app@sha256:x": "registry.example.com",
	} {
		if got := registryHost(image); got != want {
			t.Fatalf("registryHost(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestCheckRegistryHostRefusesNonPublicAddresses(t *testing.T) {
	for address, ok := range map[string]bool{
		"93.184.216.34": true, "10.1.2.3": false, "169.254.169.254": false, "::ffff:10.0.0.1": false, "198.18.0.1": false,
	} {
		lookup := func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}
		if err := checkRegistryHost(context.Background(), lookup, "registry.example.com"); (err == nil) != ok {
			t.Fatalf("checkRegistryHost(%s) = %v", address, err)
		}
	}
}
