package daemon

import (
	"bytes"
	"context"
	"path"
	"strings"
)

const npmProjectConfigFile = ".npmrc"

func npmConfigCandidates(directory string) []string {
	candidates := []string{npmProjectConfigFile}
	if directory == "." {
		return candidates
	}
	prefix := ""
	for _, component := range strings.Split(directory, "/") {
		prefix = path.Join(prefix, component)
		candidates = append(candidates, path.Join(prefix, npmProjectConfigFile))
	}
	return candidates
}

func publishedNPMConfig(ctx context.Context, trusted *trustedResultRepository, commit, directory string) (string, bool, error) {
	candidates := npmConfigCandidates(directory)
	options := trustedGitOptions{gitDir: trusted.gitDir, env: []string{"GIT_LITERAL_PATHSPECS=1"}}
	raw, err := trusted.gitAt(ctx, options, append([]string{"ls-tree", "-z", "--name-only", "--full-tree", commit, "--"}, candidates...)...)
	if err != nil {
		return "", false, err
	}
	published := map[string]bool{}
	for _, name := range bytes.Split(raw, []byte{0}) {
		published[string(name)] = true
	}
	for _, candidate := range candidates {
		if published[candidate] {
			return candidate, true, nil
		}
	}
	return "", false, nil
}
