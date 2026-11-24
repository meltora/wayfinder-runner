package repo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/example/app-runner/internal/validation"
)

var ErrPinnedCommitNeedsCredential = errors.New("pinned commit is not cached and cannot be fetched without a repository credential")

func (m *Manager) CreatePinnedWorkspace(ctx context.Context, url, sha, dest string, credential *GitHTTPSCredential) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitCommitSHA("commit_sha", sha); err != nil {
		return err
	}
	_, canFetch := applicableHTTPSCredential(url, credential)
	mirror := m.mirrorDir(url)
	if _, err := os.Lstat(mirror); errors.Is(err, os.ErrNotExist) {
		if !canFetch {
			return ErrPinnedCommitNeedsCredential
		}
		if _, err := m.RegisterWithCredential(ctx, url, credential); err != nil {
			return fmt.Errorf("ensure repository mirror: %w", err)
		}
	} else if err != nil {
		return err
	}
	resolved, err := m.runGitWithProtocols(ctx, mirror, false, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err == nil && resolved == sha {
		return m.CreateDetachedWorkspaceWithCredential(ctx, url, sha, dest, nil)
	}
	if !canFetch {
		return ErrPinnedCommitNeedsCredential
	}
	return m.CreateDetachedWorkspaceWithCredential(ctx, url, sha, dest, credential)
}
