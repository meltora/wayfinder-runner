package previewobserver

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func targetRegistry(t *testing.T, privateCIDRs []string, addresses ...string) *Registry {
	t.Helper()
	profile := observerProfileConfig()
	profile.AllowedPrivateCIDRs = privateCIDRs
	registry, err := NewRegistry(
		map[string]config.PreviewObservationProfileConfig{"node-preview": profile},
		Dependencies{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			result := make([]netip.Addr, 0, len(addresses))
			for _, raw := range addresses {
				result = append(result, netip.MustParseAddr(raw))
			}
			return result, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func targetOperation(rawURL string) v1.PreviewObservationOperation {
	return v1.PreviewObservationOperation{
		Blueprint: v1.PreviewObservationBlueprint{
			BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
		},
		PreviewURL: rawURL, RequiredHealthCheckIDs: []string{"api", "root"},
	}
}

func TestResolveTargetPinsAllowedPublicAddressAndHostname(t *testing.T) {
	registry := targetRegistry(t, nil, "93.184.216.35", "93.184.216.34")
	compiled, _ := registry.profileFor(targetOperation("https://branch.preview.example.com/review"))
	target, err := registry.resolveTarget(context.Background(), compiled, "https://branch.preview.example.com/review")
	if err != nil {
		t.Fatal(err)
	}
	if target.hostname != "branch.preview.example.com" || target.address.String() != "93.184.216.34" {
		t.Fatalf("target = %#v", target)
	}
}

func TestResolveTargetRejectsURLAuthoritySmuggling(t *testing.T) {
	registry := targetRegistry(t, nil, "93.184.216.34")
	compiled, _ := registry.profileFor(targetOperation("https://branch.preview.example.com"))
	for _, raw := range []string{
		"http://branch.preview.example.com",
		"https://user:secret@branch.preview.example.com",
		"https://branch.preview.example.com:443",
		"https://branch.preview.example.com?token=secret",
		"https://branch.preview.example.com#fragment",
		"https://branch.preview.example.com\\@evil.example",
		"https://preview.example.com.evil.example",
		"https://127.0.0.1",
		"https://localhost",
		"https://unqualified",
		"https://branch..preview.example.com",
		"https://-branch.preview.example.com",
		"https://BRANCH.preview.example.com",
		"https://branch.preview.example.com.",
	} {
		if _, err := registry.resolveTarget(context.Background(), compiled, raw); err == nil ||
			resultCode(err) != v1.PreviewTargetNotAllowed {
			t.Fatalf("unsafe target %q result = %v", raw, err)
		}
	}
}

func TestResolveTargetRejectsAnyDisallowedDNSAnswer(t *testing.T) {
	registry := targetRegistry(t, nil, "93.184.216.34", "10.40.0.5")
	compiled, _ := registry.profileFor(targetOperation("https://branch.preview.example.com"))
	if _, err := registry.resolveTarget(context.Background(), compiled, "https://branch.preview.example.com"); err == nil ||
		resultCode(err) != v1.PreviewTargetNotAllowed {
		t.Fatalf("mixed DNS answer result = %v", err)
	}
}

func TestResolveTargetRejectsUnboundedDNSAnswerSet(t *testing.T) {
	addresses := make([]string, 33)
	for index := range addresses {
		addresses[index] = "93.184.216." + strconv.Itoa(index+1)
	}
	registry := targetRegistry(t, nil, addresses...)
	compiled, _ := registry.profileFor(targetOperation("https://branch.preview.example.com"))
	if _, err := registry.resolveTarget(context.Background(), compiled, "https://branch.preview.example.com"); err == nil ||
		resultCode(err) != v1.PreviewInvalidObserverResponse {
		t.Fatalf("unbounded DNS answer result = %v", err)
	}
}

func TestResolveTargetAllowsOnlyExplicitPrivateCIDR(t *testing.T) {
	for name, registry := range map[string]*Registry{
		"allowed":     targetRegistry(t, []string{"10.40.0.0/16"}, "10.40.2.8"),
		"foreign":     targetRegistry(t, []string{"10.40.0.0/16"}, "10.41.2.8"),
		"loopback":    targetRegistry(t, []string{"10.40.0.0/16"}, "127.0.0.1"),
		"link-local":  targetRegistry(t, []string{"10.40.0.0/16"}, "169.254.169.254"),
		"nat64":       targetRegistry(t, []string{"10.40.0.0/16"}, "64:ff9b::a28:5"),
		"six-to-four": targetRegistry(t, []string{"10.40.0.0/16"}, "2002:0a28:0005::1"),
	} {
		compiled, _ := registry.profileFor(targetOperation("https://branch.preview.example.com"))
		_, err := registry.resolveTarget(context.Background(), compiled, "https://branch.preview.example.com")
		if name == "allowed" && err != nil {
			t.Fatalf("allowed private target rejected: %v", err)
		}
		if name != "allowed" && (err == nil || resultCode(err) != v1.PreviewTargetNotAllowed) {
			t.Fatalf("%s private target result = %v", name, err)
		}
	}
}
