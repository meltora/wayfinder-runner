package elevatedcapability

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type keyContractCases struct {
	Accepted []string `json:"accepted"`
	Rejected []string `json:"rejected"`
}

type keyContract struct {
	NetworkAccess  keyContractCases `json:"networkAccess"`
	RepositoryPath keyContractCases `json:"repositoryPath"`
}

func loadKeyContract(t *testing.T) keyContract {
	t.Helper()
	raw, err := os.ReadFile("testdata/elevated-capability-key-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract keyContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func contractGrant(kind v1.ElevatedCapabilityKind, key string) v1.ElevatedCapabilityGrant {
	return grant("ecr_contract", kind, key, strings.Repeat("a", 64))
}

func TestNetworkKeyContractMatchesApp(t *testing.T) {
	contract := loadKeyContract(t)
	for _, key := range contract.NetworkAccess.Accepted {
		if _, ok := NormalizeHost(key); !ok || !GrantShapeValid(contractGrant(v1.ElevatedCapabilityNetworkAccess, key)) {
			t.Errorf("network key %q refused", key)
		}
	}
	for _, key := range contract.NetworkAccess.Rejected {
		if _, ok := NormalizeHost(key); ok {
			t.Errorf("network key %q accepted", key)
		}
	}
}

func TestRepositoryKeyContractMatchesApp(t *testing.T) {
	contract := loadKeyContract(t)
	for _, key := range contract.RepositoryPath.Accepted {
		if !GrantShapeValid(contractGrant(v1.ElevatedCapabilityMCPServer, key)) {
			t.Errorf("repository key %q refused", key)
		}
	}
	for _, key := range contract.RepositoryPath.Rejected {
		if GrantShapeValid(contractGrant(v1.ElevatedCapabilityMCPServer, key)) {
			t.Errorf("repository key %q accepted", key)
		}
	}
}
