package previewhost

import (
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

func TestPublicAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"93.184.216.34":   true,
		"2606:4700::1111": true,
		"10.1.2.3":        false,
		"172.16.0.1":      false,
		"192.168.1.1":     false,
		"127.0.0.1":       false,
		"169.254.169.254": false,
		"100.64.0.1":      false,
		"0.0.0.0":         false,
		"::1":             false,
		"fe80::1":         false,
		"fd00::1":         false,
		"fd00:ec2::254":   false,
		"224.0.0.1":       false,
		"198.18.0.1":      false,
		"64:ff9b::a00:1":  false,
		"::ffff:10.0.0.1": false,
	} {
		if got := publicAddress(netip.MustParseAddr(address)); got != want {
			t.Fatalf("publicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}
