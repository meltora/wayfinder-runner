package provider

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func claudeSandboxNetwork(t *testing.T, p StartParams) map[string]any {
	t.Helper()
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	return readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))["sandbox"].(map[string]any)["network"].(map[string]any)
}

func TestClaudeSandboxAllowsOurPreviewOnlyWhenReachIsOpen(t *testing.T) {
	for name, tc := range map[string]struct {
		reach    bool
		elevated bool
		want     []any
	}{
		"closed":            {false, false, []any{}},
		"open":              {true, false, []any{"our-preview"}},
		"closed elevated":   {false, true, []any{"registry.npmjs.org"}},
		"open with elevate": {true, true, []any{"our-preview", "registry.npmjs.org"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := testParams(t)
			p.Job.Kind = v1.JobKindBuild
			p.DockerIsolation = true
			p.PreviewReach = tc.reach
			if tc.elevated {
				p.Elevated = testElevatedGrants()
			}
			network := claudeSandboxNetwork(t, p)
			if network["strictAllowlist"] != true {
				t.Fatalf("strictAllowlist = %#v", network["strictAllowlist"])
			}
			if !reflect.DeepEqual(network["allowedDomains"], tc.want) {
				t.Fatalf("allowedDomains = %#v want %#v", network["allowedDomains"], tc.want)
			}
		})
	}
}

func TestClaudeSandboxPreviewReachKeepsOtherHostsBlocked(t *testing.T) {
	for name, tc := range map[string]struct {
		elevated bool
		want     []any
	}{
		"reach only":         {false, []any{v1.PreviewJobDeployInternalHost}},
		"reach and elevated": {true, []any{v1.PreviewJobDeployInternalHost, "registry.npmjs.org"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := testParams(t)
			p.Job.Kind = v1.JobKindBuild
			p.DockerIsolation = true
			p.PreviewReach = true
			if tc.elevated {
				p.Elevated = testElevatedGrants()
			}
			network := claudeSandboxNetwork(t, p)
			if network["strictAllowlist"] != true {
				t.Fatalf("strictAllowlist = %#v", network["strictAllowlist"])
			}
			for key := range network {
				if key != "strictAllowlist" && key != "allowedDomains" {
					t.Fatalf("sandbox network gained %q: %#v", key, network)
				}
			}
			domains := network["allowedDomains"].([]any)
			if !reflect.DeepEqual(domains, tc.want) {
				t.Fatalf("allowedDomains = %#v want %#v", domains, tc.want)
			}
			for _, domain := range domains {
				value := domain.(string)
				if strings.ContainsAny(value, "*?") || strings.HasPrefix(value, ".") {
					t.Fatalf("allowedDomains carries a wildcard: %q", value)
				}
			}
			for _, blocked := range []string{"example.com", "our-preview.example.com", "evil-our-preview", "host.docker.internal", "localhost", "169.254.169.254"} {
				for _, domain := range domains {
					if domain == blocked {
						t.Fatalf("allowedDomains allows %q", blocked)
					}
				}
			}
		})
	}
}

func TestCodexProfileNetworkStaysDisabledRegardlessOfPreviewReach(t *testing.T) {
	p := testParams(t)
	p.Job.Kind = v1.JobKindBuild
	p.DockerIsolation = true
	closed, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	p.PreviewReach = true
	open, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	if !reflect.DeepEqual(open.Args, closed.Args) || !reflect.DeepEqual(open.Env, closed.Env) {
		t.Fatalf("codex spec changed with preview reach:\n%v\n%v", open.Args, closed.Args)
	}
	for _, arg := range open.Args {
		if strings.Contains(arg, "network_access=true") || strings.Contains(arg, "danger-full-access") {
			t.Fatalf("codex args open the network: %v", open.Args)
		}
	}
}
