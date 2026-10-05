package capability

import (
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/validation"
	"sort"
)

func scanCapabilityRepositories(ids []string) []string {
	if len(ids) == 0 || len(ids) > 256 {
		return nil
	}
	copyIDs := append([]string(nil), ids...)
	sort.Strings(copyIDs)
	for i, id := range copyIDs {
		if validation.ID("repository_id", id) != nil || (i > 0 && copyIDs[i-1] == id) {
			return nil
		}
	}
	return copyIDs
}

func scanCapabilitySupportedOS(platform string) bool {
	return repo.AgentConfigScanSupportedOS(platform)
}
