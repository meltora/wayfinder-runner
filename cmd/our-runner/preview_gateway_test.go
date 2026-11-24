package main

import (
	"slices"
	"testing"
)

func TestParsePreviewGatewayFlags(t *testing.T) {
	cfg, err := parsePreviewGatewayFlags([]string{
		"--ingress-listen", ":8080", "--ingress-target", "app:3000",
		"--egress-listen", ":3128", "--allow=registry.npmjs.org", "--allow", "api.github.com",
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

func TestRunCLIDispatchesPreviewGateway(t *testing.T) {
	if code := runCLI([]string{"preview-gateway"}); code == 0 {
		t.Fatal("preview-gateway without flags succeeded")
	}
}
