package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/validation"
)

var ErrPinnedCommitNeedsCredential = errors.New("pinned commit is not cached and cannot be fetched without a repository credential")

var errPinnedCommitNotCached = errors.New("pinned commit is not cached")

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
	err := m.createDetachedWorkspaceFromCache(ctx, url, sha, dest)
	if !errors.Is(err, errPinnedCommitNotCached) {
		return err
	}
	if !canFetch {
		return ErrPinnedCommitNeedsCredential
	}
	return m.CreateDetachedWorkspaceWithCredential(ctx, url, sha, dest, credential)
}

func (m *Manager) createDetachedWorkspaceFromCache(ctx context.Context, url, sha, dest string) error {
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("workspace destination already exists")
	}
	mirror := m.mirrorDir(url)
	resolved, err := m.runGitWithProtocols(ctx, mirror, false, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil || resolved != sha {
		return errPinnedCommitNotCached
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if _, err := m.runGitWithProtocols(ctx, "", true, "clone", "--no-hardlinks", "--no-checkout", mirror, dest); err != nil {
		return err
	}
	if _, err := m.runGitWithProtocols(ctx, dest, true, "checkout", "--detach", sha); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	got, err := m.runGitWithProtocols(ctx, dest, false, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || got != sha {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("pinned checkout mismatch: got %q want %q", got, sha)
	}
	return nil
}
