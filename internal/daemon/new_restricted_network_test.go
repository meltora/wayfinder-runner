package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const restrictedTestNetworkID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func installRestrictedNetworkDocker(t *testing.T, networkJSON string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$1" in
  info) echo '{"ServerVersion":"test","Driver":"overlay2","SecurityOptions":["name=rootless"]}' ;;
  network)
    if [ -z '` + networkJSON + `' ]; then echo "Error: No such network: $5" 1>&2; exit 1; fi
    echo '` + networkJSON + `' ;;
  ps) exit 0 ;;
  rm) exit 0 ;;
  run) printf '%b' "` + rootlessMapOutput() + `" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func restrictedNetworkJSON(driver, scope, enforced, policy string) string {
	return `{"Id":"` + restrictedTestNetworkID + `","Name":"our-runner-jobs-dev","Driver":"` + driver + `","Scope":"` + scope +
		`","Internal":false,"Attachable":false,"Ingress":false,"Labels":{"com.ourplatform.egress-enforced":"` + enforced +
		`","com.ourplatform.egress-policy":"` + policy + `"}}`
}

func enrolledRestrictedDockerConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := enrolledDirectConfig(t)
	cfg.Isolation = "docker"
	cfg.AllowUnsafeDirect = false
	cfg.JobImage = "ghcr.io/example/job@sha256:" + strings.Repeat("a", 64)
	cfg.JobNetwork = "restricted"
	cfg.JobNetworkName = "our-runner-jobs-dev"
	cfg.EgressPolicyID = "local-dev-egress-not-enforced"
	return config.Normalize(cfg)
}

func TestNewRefusesToStartWhenTheRestrictedNetworkFailsAttestation(t *testing.T) {
	for name, test := range map[string]struct{ networkJSON, want string }{
		"missing network":      {"", "inspect restricted Docker network"},
		"policy label differs": {restrictedNetworkJSON("bridge", "local", "true", "other-policy"), `not bound to enforced egress policy "local-dev-egress-not-enforced"`},
		"egress not enforced":  {restrictedNetworkJSON("bridge", "local", "false", "local-dev-egress-not-enforced"), "not bound to enforced egress policy"},
		"ineligible driver":    {restrictedNetworkJSON("overlay", "local", "true", "local-dev-egress-not-enforced"), "topology is not eligible"},
		"ineligible scope":     {restrictedNetworkJSON("bridge", "swarm", "true", "local-dev-egress-not-enforced"), "topology is not eligible"},
	} {
		t.Run(name, func(t *testing.T) {
			installRestrictedNetworkDocker(t, test.networkJSON)
			d, err := New(enrolledRestrictedDockerConfig(t), nil)
			if err == nil {
				closeConstructedDaemon(d)
				t.Fatal("daemon started on an unattested restricted network")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewStartsOnAnAttestedRestrictedNetwork(t *testing.T) {
	installRestrictedNetworkDocker(t, restrictedNetworkJSON("bridge", "local", "true", "local-dev-egress-not-enforced"))
	d, err := New(enrolledRestrictedDockerConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.cfg.JobNetwork != "restricted" || d.cfg.JobNetworkName != "our-runner-jobs-dev" || d.cfg.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("cfg = %+v", d.cfg)
	}
}
