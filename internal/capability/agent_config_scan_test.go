package capability

import (
	"context"
	"errors"
	v1 "github.com/example/app-runner/api/runner/v1"
	"reflect"
	"testing"
)

func TestAgentConfigScanCapabilityRequiresGitAndBoundedRegistrations(t *testing.T) {
	d := fakeDetector()
	d.AgentConfigScanRepositoryIDs = []string{"repository-z", "repository-a"}
	m := d.Collect(context.Background())
	if m.AgentConfigScanTransportVersion != v1.AgentConfigScanTransportVersion || !reflect.DeepEqual(m.AgentConfigScanRepositoryIDs, []string{"repository-a", "repository-z"}) {
		t.Fatalf("capability=%+v", m)
	}
	if d.AgentConfigScanRepositoryIDs[0] != "repository-z" {
		t.Fatal("mutated input")
	}
	for _, ids := range [][]string{nil, {"invalid/id"}, {"same", "same"}, make([]string, 257)} {
		d.AgentConfigScanRepositoryIDs = ids
		m = d.Collect(context.Background())
		if m.AgentConfigScanTransportVersion != "" || len(m.AgentConfigScanRepositoryIDs) != 0 {
			t.Fatal("invalid registrations advertised")
		}
	}
	d.AgentConfigScanRepositoryIDs = []string{"repository-1"}
	d.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	m = d.Collect(context.Background())
	if m.AgentConfigScanTransportVersion != "" {
		t.Fatal("missing Git advertised")
	}
}

func TestAgentConfigScanCapabilityOnlySupportedProcessGroupPlatforms(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		if !scanCapabilitySupportedOS(platform) {
			t.Fatal("supported platform omitted")
		}
	}
	for _, platform := range []string{"windows", "plan9", "unknown"} {
		if scanCapabilitySupportedOS(platform) {
			t.Fatal("platform without verified transport cancellation advertised")
		}
	}
}
