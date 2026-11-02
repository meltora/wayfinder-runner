package daemon

import (
	"bytes"
	"context"
	"path"
	"strings"
)

const npmProjectConfigFile = ".npmrc"

func npmConfigCandidates(directory string) []string {
	return ancestorCandidates(directory, npmProjectConfigFile, true)
}

func ancestorCandidates(directory, name string, includeDirectory bool) []string {
	directories := []string{"."}
	if directory != "." {
		prefix := ""
		for _, component := range strings.Split(directory, "/") {
			prefix = path.Join(prefix, component)
			directories = append(directories, prefix)
		}
	}
	if !includeDirectory {
		directories = directories[:len(directories)-1]
	}
	var candidates []string
	for _, ancestor := range directories {
		candidates = append(candidates, path.Join(ancestor, name))
	}
	return candidates
}

func publishedNPMConfig(ctx context.Context, trusted *trustedResultRepository, commit, directory string) (string, bool, error) {
	return firstPublishedPath(ctx, trusted, commit, npmConfigCandidates(directory))
}

func publishedAncestorManifest(ctx context.Context, trusted *trustedResultRepository, commit, directory string) (string, bool, error) {
	return firstPublishedPath(ctx, trusted, commit, ancestorCandidates(directory, npmPackageManifestFile, false))
}

func firstPublishedPath(ctx context.Context, trusted *trustedResultRepository, commit string, candidates []string) (string, bool, error) {
	if len(candidates) == 0 {
		return "", false, nil
	}
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
