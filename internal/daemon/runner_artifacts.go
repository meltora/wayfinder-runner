package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	gitDir := filepath.Join(checkout, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runner artifacts exclude: checkout git directory is not a directory")
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	if info, err := os.Lstat(infoDir); err != nil || !info.IsDir() {
		return fmt.Errorf("runner artifacts exclude: info is not a directory")
	}
	excludePath := filepath.Join(infoDir, "exclude")
	content, err := os.ReadFile(excludePath)
	switch {
	case err == nil:
		if info, statErr := os.Lstat(excludePath); statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("runner artifacts exclude: exclude is not a regular file")
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == runnerArtifactsExcludeEntry {
			return nil
		}
	}
	addition := runnerArtifactsExcludeEntry + "\n"
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		addition = "\n" + addition
	}
	file, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, fs.FileMode(0o644))
	if err != nil {
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	if _, err := file.WriteString(addition); err != nil {
		_ = file.Close()
		return fmt.Errorf("runner artifacts exclude: %w", err)
	}
	return file.Close()
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
