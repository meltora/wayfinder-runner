package capability

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestCollectEmptyPackTrustIsAnExplicitArray(t *testing.T) {
	detector := New("runner-enrollment", "1.0.0", map[string]string{}, map[string]string{})
	detector.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	detector.AuthReady = func(string) bool { return false }
	manifest := detector.Collect(context.Background())
	if manifest.TrustedAgentPackKeyIDs == nil {
		t.Fatal("fresh enrollment must report an empty trust array, not null")
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["trusted_agent_pack_key_ids"]) != "[]" {
		t.Fatal("wire trust list must be an explicit empty array")
	}
	if string(fields["agent_pack_delivery_authority_version"]) != "1" {
		t.Fatal("empty trust does not remove protocol support")
	}
}
