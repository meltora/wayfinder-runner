package repo

import (
	"context"
	"fmt"
	"os"

	"github.com/example/app-runner/internal/validation"
)

const (
	PreviewExportRefPrefix = "refs/our-preview-export/"
	PreviewMirrorRefPrefix = "refs/our-preview/"
)

func (m *Manager) HasCommit(ctx context.Context, url, sha string) bool {
	if validateURL(url, m.AllowLocal) != nil || validation.GitCommitSHA("commit_sha", sha) != nil {
		return false
	}
	resolved, err := m.runGitWithProtocols(ctx, m.mirrorDir(url), false, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	return err == nil && resolved == sha
}

func (m *Manager) ImportPreviewCommit(ctx context.Context, url, bundlePath, sha string) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitCommitSHA("commit_sha", sha); err != nil {
		return err
	}
	info, err := os.Lstat(bundlePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 {
		return fmt.Errorf("git bundle must be a regular file between 1 byte and 512 MiB")
	}
	mirror := m.mirrorDir(url)
	if info, err := os.Lstat(mirror); err != nil || !info.IsDir() {
		return fmt.Errorf("repository mirror is not registered")
	}
	ref := PreviewMirrorRefPrefix + sha
	refspec := PreviewExportRefPrefix + sha + ":" + ref
	if _, err := m.runGitWithProtocols(ctx, mirror, true, "fetch", "--no-tags", "--force", bundlePath, refspec); err != nil {
		return err
	}
	got, err := m.runGitWithProtocols(ctx, mirror, false, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return err
	}
	if got != sha {
		return fmt.Errorf("imported preview commit mismatch: got %s want %s", got, sha)
	}
	return nil
}
