package daemon

import (
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	runnerArtifactsDirectory    = "shortstack-artifacts"
	runnerArtifactsExcludeEntry = runnerArtifactsDirectory + "/"
	runnerArtifactsPathspec     = ":(exclude,glob)**/" + runnerArtifactsDirectory + "/**"
)

func ensureRunnerArtifactsExcluded(checkout string) error {
	if err := ensureGitExcludeEntries(checkout, ".", runnerArtifactsExcludeEntry); err != nil {
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	return nil
}

func runnerArtifactPath(path string) bool {
	fold := cases.Fold()
	for _, segment := range strings.Split(path, "/") {
		if norm.NFD.String(fold.String(norm.NFD.String(segment))) == runnerArtifactsDirectory {
			return true
		}
	}
	return false
}
