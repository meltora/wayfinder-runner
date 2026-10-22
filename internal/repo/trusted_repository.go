package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/validation"
)

func (m *Manager) CloneTrustedRepository(ctx context.Context, url, commitSHA, dest string) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitCommitSHA("start_sha", commitSHA); err != nil {
		return err
	}
	if dest == "" || !filepath.IsAbs(dest) {
		return fmt.Errorf("trusted repository path must be absolute")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("trusted repository destination already exists")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if _, err := m.runGitWithProtocols(ctx, "", true, "clone", "--bare", "--local", "--quiet", m.mirrorDir(url), dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	got, err := m.runGitWithProtocols(ctx, dest, false, "rev-parse", "--verify", "--quiet", commitSHA+"^{commit}")
	if err != nil || got != commitSHA {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("trusted repository does not contain the start commit")
	}
	return nil
}
