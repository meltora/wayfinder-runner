package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const ParserVersion = "runner-scanner-v1"

type DiscoveryInput struct {
	ProjectID    string
	RepositoryID string
	BranchRef    string
	CommitSHA    string
	ObservedAt   time.Time
}

type semanticReport struct {
	ProjectID      string                        `json:"project_id"`
	RepositoryID   string                        `json:"repository_id"`
	BranchRef      string                        `json:"branch_ref"`
	CommitSHA      string                        `json:"commit_sha"`
	CatalogVersion string                        `json:"catalog_version"`
	ParserVersion  string                        `json:"parser_version"`
	Items          []v1.AgentConfigDiscoveryItem `json:"items"`
}

func BuildDiscoveryEvent(input DiscoveryInput, inventory Inventory) (v1.AgentConfigDiscoveryEvent, error) {
	items := cloneItems(inventory.Items)
	semantic := semanticReport{
		ProjectID: input.ProjectID, RepositoryID: input.RepositoryID,
		BranchRef: input.BranchRef, CommitSHA: input.CommitSHA,
		CatalogVersion: v1.AgentConfigCatalogVersion, ParserVersion: ParserVersion, Items: items,
	}
	raw, err := json.Marshal(semantic)
	if err != nil {
		return v1.AgentConfigDiscoveryEvent{}, fmt.Errorf("encode discovery identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return v1.AgentConfigDiscoveryEvent{
		Kind: v1.EventKindAgentConfigDiscovery, ScanID: "scan-" + hex.EncodeToString(digest[:]),
		ProjectID: input.ProjectID, RepositoryID: input.RepositoryID,
		BranchRef: input.BranchRef, CommitSHA: input.CommitSHA,
		CatalogVersion: v1.AgentConfigCatalogVersion, ParserVersion: ParserVersion,
		ObservedAt: input.ObservedAt.UTC(), Items: items,
	}, nil
}

func DiscoveryEventID(scanID string) string {
	return "agent-discovery-" + strings.TrimPrefix(scanID, "scan-")
}

func cloneItems(items []v1.AgentConfigDiscoveryItem) []v1.AgentConfigDiscoveryItem {
	cloned := make([]v1.AgentConfigDiscoveryItem, len(items))
	for index, item := range items {
		item.RequestedPermissions = append([]string{}, item.RequestedPermissions...)
		item.Conflicts = append([]string{}, item.Conflicts...)
		cloned[index] = item
	}
	return cloned
}
