package main

import (
	"net/netip"
	"slices"
	"testing"
)

func TestParsePreviewGatewayFlags(t *testing.T) {
	cfg, err := parsePreviewGatewayFlags([]string{
		"--ingress-listen", ":8080", "--ingress-target", "app:3000",
		"--egress-listen", ":3128", "--allow=registry.npmjs.org", "--allow", "api.github.com",
		"--egress-source", "172.25.0.0/16",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IngressListen != ":8080" || cfg.IngressTarget != "app:3000" || cfg.EgressListen != ":3128" ||
		!slices.Equal(cfg.Allow, []string{"registry.npmjs.org", "api.github.com"}) || cfg.AllowAddress != nil {
		t.Fatalf("cfg = %+v", cfg)
	}
	none, err := parsePreviewGatewayFlags([]string{"--ingress-listen", ":8080", "--ingress-target", "app:3000"})
	if err != nil || none.EgressListen != "" || len(none.Allow) != 0 {
		t.Fatalf("cfg = %+v err = %v", none, err)
	}
	for name, args := range map[string][]string{
		"no ingress":           {"--ingress-target", "app:3000"},
		"no target":            {"--ingress-listen", ":8080"},
		"allow without egress": {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--allow", "a.example.com"},
		"empty allow":          {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128", "--allow="},
		"positional":           {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "extra"},
		"unknown flag":         {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--privileged"},
	} {
		if _, err := parsePreviewGatewayFlags(args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestParsePreviewGatewayFlagsEgressSource(t *testing.T) {
	cfg, err := parsePreviewGatewayFlags([]string{
		"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128",
		"--egress-source", "172.25.0.0/16", "--egress-source=10.1.2.3/24", "--egress-source", "fd00:1::/64",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("172.25.0.0/16"), netip.MustParsePrefix("10.1.2.0/24"), netip.MustParsePrefix("fd00:1::/64"),
	}
	if !slices.Equal(cfg.EgressSources, want) {
		t.Fatalf("egress sources = %v", cfg.EgressSources)
	}
	for name, args := range map[string][]string{
		"egress without source": {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128"},
		"allow without source": {
			"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128", "--allow", "a.example.com",
		},
		"source without egress": {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-source", "10.0.0.0/8"},
		"empty source":          {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128", "--egress-source="},
		"bare address":          {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128", "--egress-source", "10.0.0.1"},
		"not a prefix":          {"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128", "--egress-source", "preview"},
	} {
		if _, err := parsePreviewGatewayFlags(args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestRunCLIDispatchesPreviewGateway(t *testing.T) {
	if code := runCLI([]string{"preview-gateway"}); code == 0 {
		t.Fatal("preview-gateway without flags succeeded")
	}
}
