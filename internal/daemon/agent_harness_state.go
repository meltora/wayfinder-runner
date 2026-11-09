package daemon

import (
	"context"
	"maps"
	"slices"
)

var agentHarnessState = []string{".claude/.cc-writes"}

func agentHarnessStatePaths() []string {
	return slices.Clone(agentHarnessState)
}

func (t *trustedResultRepository) untrackedHarnessState(ctx context.Context, start string) (map[string]bool, error) {
	skip := map[string]bool{}
	for _, rel := range agentHarnessState {
		listed, err := t.git(ctx, "ls-tree", "-z", "--name-only", start, "--", rel)
		if err != nil {
			return nil, err
		}
		if len(listed) == 0 {
			skip[rel] = true
		}
	}
	return skip, nil
}

func snapshotSkips(candidates []agentCandidate, parent string, harness map[string]bool) map[string]bool {
	skip := nestedCandidates(candidates, parent)
	maps.Copy(skip, harness)
	return skip
}
