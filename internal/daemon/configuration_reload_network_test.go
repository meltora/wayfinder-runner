package daemon

import (
	"testing"

	"github.com/example/app-runner/internal/config"
)

func TestRestartRequiredSettingsIncludeTheRestrictedNetworkIdentity(t *testing.T) {
	running := config.Default()
	running.Isolation = "docker"
	running.JobNetwork = "restricted"
	running.JobNetworkName = "our-runner-jobs-dev"
	running.EgressPolicyID = "local-dev-egress-not-enforced"
	if !restartRequiredSettingsEqual(running, running) {
		t.Fatal("identical restricted settings were treated as changed")
	}
	for name, change := range map[string]func(*config.Config){
		"job_network_name": func(c *config.Config) { c.JobNetworkName = "our-runner-jobs-other" },
		"egress_policy_id": func(c *config.Config) { c.EgressPolicyID = "other-policy" },
	} {
		next := running
		change(&next)
		if restartRequiredSettingsEqual(running, next) {
			t.Fatalf("%s change was treated as hot", name)
		}
	}
	hot := running
	hot.Labels = map[string]string{"pool": "other"}
	if !restartRequiredSettingsEqual(running, hot) {
		t.Fatal("a label change with an unchanged restricted network was treated as restart-required")
	}
}
