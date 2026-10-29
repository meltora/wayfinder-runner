package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/supervisor"
)

const (
	agentPromptDirectoryName = "prompts"
	agentPromptFilePrefix    = "prompt-"
)

type agentPromptFile struct {
	path     string
	stateDir string
	name     string
}

func agentPromptDirectory(stateDir string) string {
	return filepath.Join(stateDir, "supervisor", agentPromptDirectoryName)
}

func openAgentPromptDirectory(stateDir string) (*os.Root, error) {
	supervisorRoot := filepath.Dir(agentPromptDirectory(stateDir))
	if err := os.MkdirAll(supervisorRoot, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(supervisorRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(agentPromptDirectoryName, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := root.Lstat(agentPromptDirectoryName)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent prompt directory is not a directory")
	}
	if err := root.Chmod(agentPromptDirectoryName, 0o700); err != nil {
		return nil, err
	}
	return root.OpenRoot(agentPromptDirectoryName)
}

func writeAgentPromptFile(stateDir, attemptID, prompt string) (*agentPromptFile, error) {
	dir, err := openAgentPromptDirectory(stateDir)
	if err != nil {
		return nil, fmt.Errorf("prepare agent prompt directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	name := agentPromptFilePrefix + attemptID + "-" + hex.EncodeToString(suffix)
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create agent prompt file: %w", err)
	}
	prompted := &agentPromptFile{path: filepath.Join(agentPromptDirectory(stateDir), name), stateDir: stateDir, name: name}
	_, writeErr := io.WriteString(file, prompt)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = prompted.Remove()
		return nil, fmt.Errorf("write agent prompt file: %w", writeErr)
	}
	return prompted, nil
}

func (f *agentPromptFile) Remove() error {
	if f == nil {
		return nil
	}
	dir, err := openExistingAgentPromptDirectory(f.stateDir)
	if err != nil || dir == nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Remove(f.name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func openExistingAgentPromptDirectory(stateDir string) (*os.Root, error) {
	supervisorRoot, err := os.OpenRoot(filepath.Dir(agentPromptDirectory(stateDir)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = supervisorRoot.Close() }()
	info, err := supervisorRoot.Lstat(agentPromptDirectoryName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent prompt directory is not a directory")
	}
	return supervisorRoot.OpenRoot(agentPromptDirectoryName)
}

func (jr *jobRunner) attachAgentPromptFile(spec *supervisor.Spec) (*agentPromptFile, error) {
	if spec.Stdin == nil || spec.StdinFile != "" {
		return nil, fmt.Errorf("agent start specification does not deliver its prompt on stdin")
	}
	limit := jr.agentPromptByteLimit()
	raw, err := io.ReadAll(io.LimitReader(spec.Stdin, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if !agentPromptWithinLimit(string(raw), limit) {
		return nil, fmt.Errorf("agent prompt exceeds the runner size limit")
	}
	file, err := writeAgentPromptFile(jr.d.cfg.StateDir, jr.job.AttemptID, string(raw))
	if err != nil {
		return nil, err
	}
	spec.Stdin = nil
	spec.StdinFile = file.path
	return file, nil
}

func sweepAgentPromptFiles(stateDir string) error {
	dir, err := openExistingAgentPromptDirectory(stateDir)
	if err != nil || dir == nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	listing, err := dir.Open(".")
	if err != nil {
		return err
	}
	entries, err := listing.ReadDir(-1)
	_ = listing.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), agentPromptFilePrefix) {
			continue
		}
		if err := dir.Remove(entry.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
