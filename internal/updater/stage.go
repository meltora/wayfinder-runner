package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func (c *Coordinator) stage(ctx context.Context, r Release) (string, error) {
	if err := c.checkDisk(r); err != nil {
		return "", err
	}
	slotName := fmt.Sprintf("%020d-%s", r.Sequence, r.Version)
	final := filepath.Join(c.cfg.VersionsDir, slotName)
	if info, err := os.Lstat(final); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("prior slot is not a protected directory")
		}
		if c.cfg.RequireRootOwnership && !ownedByRoot(info) {
			return "", fmt.Errorf("prior slot is not root owned")
		}
		for _, link := range []string{c.cfg.CurrentLink, c.cfg.PreviousLink} {
			target, readErr := readOptionalLink(link)
			if readErr != nil {
				return "", readErr
			}
			if filepath.Clean(target) == final {
				return "", fmt.Errorf("refusing to quarantine active slot")
			}
		}
		if updaterTarget, readErr := readOptionalLink(c.cfg.UpdaterCurrentLink); readErr != nil {
			return "", readErr
		} else if updaterTarget != "" && filepath.Dir(filepath.Clean(updaterTarget)) == final {
			return "", fmt.Errorf("refusing to quarantine active updater slot")
		}
		quarantine, makeErr := os.MkdirTemp(c.cfg.VersionsDir, ".quarantine-")
		if makeErr != nil {
			return "", makeErr
		}
		if err := os.Remove(quarantine); err != nil {
			return "", err
		}
		if err := os.Rename(final, quarantine); err != nil {
			return "", fmt.Errorf("quarantine prior slot: %w", err)
		}
		if err := syncDir(c.cfg.VersionsDir); err != nil {
			return "", err
		}
	}
	download, err := os.CreateTemp(c.cfg.StateDir, "artifact-*.tgz")
	if err != nil {
		return "", err
	}
	archivePath := download.Name()
	defer func() { _ = os.Remove(archivePath) }()
	if err := download.Chmod(0o600); err != nil {
		return "", errors.Join(err, download.Close())
	}
	if err := c.artifacts.Download(ctx, r.URL, r.Size, r.SHA256, download); err != nil {
		return "", errors.Join(fmt.Errorf("download artifact: %w", err), download.Close())
	}
	if err := download.Sync(); err != nil {
		return "", errors.Join(err, download.Close())
	}
	if err := download.Close(); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(c.cfg.VersionsDir, ".stage-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := c.extractArchive(archivePath, stage); err != nil {
		return "", err
	}
	// #nosec G301,G302 -- candidate executables are root-owned and must be
	// traversable by the unprivileged preflight and runner processes.
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	if err := c.preflight.Check(ctx, stage); err != nil {
		return "", fmt.Errorf("preflight: %w", err)
	}
	for _, name := range requiredBinaries {
		// #nosec G304 -- name comes from the two-entry requiredBinaries allowlist
		// and stage is a newly created root-owned directory.
		f, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return "", err
		}
		if err := f.Sync(); err != nil {
			return "", errors.Join(err, f.Close())
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	if err := syncDir(stage); err != nil {
		return "", err
	}
	if err := os.Rename(stage, final); err != nil {
		return "", err
	}
	if err := syncDir(c.cfg.VersionsDir); err != nil {
		return "", err
	}
	ok = true
	return final, nil
}

func (c *Coordinator) extractArchive(path, dst string) error {
	// #nosec G304 -- path is the private temp file created by stage and dst is
	// a new directory beneath the root-owned versions directory.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(io.LimitReader(f, c.cfg.MaxArchiveBytes+1))
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fmt.Errorf("archive member %q is not a regular file", h.Name)
		}
		if h.Name != "our-runner" && h.Name != "our-runner-updater" {
			return fmt.Errorf("unexpected archive member %q", h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("duplicate archive member %q", h.Name)
		}
		if h.Size <= 0 || h.Size > c.cfg.MaxFileBytes {
			return fmt.Errorf("invalid size for %q", h.Name)
		}
		if h.Size > c.cfg.MaxExtractedBytes-total {
			return fmt.Errorf("archive extracted size exceeds policy")
		}
		total += h.Size
		// #nosec G305 -- h.Name was matched exactly against the two flat binary
		// names above; separators and traversal components cannot reach this line.
		target := filepath.Join(dst, h.Name)
		// #nosec G302,G304 -- the exact allowlisted executables need mode 0755 and
		// are created exclusively in the new root-owned staging directory.
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			return err
		}
		n, copyErr := io.CopyN(out, tr, h.Size)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || n != h.Size {
			return fmt.Errorf("extract %q: truncated", h.Name)
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		seen[h.Name] = true
	}
	if len(seen) != len(requiredBinaries) {
		return fmt.Errorf("archive must contain exactly our-runner and our-runner-updater")
	}
	return nil
}

func (c *Coordinator) gc(s durableState) {
	entries, err := os.ReadDir(c.cfg.VersionsDir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, p := range []string{s.PendingPath, s.PreviousTarget} {
		if p != "" {
			keep[filepath.Clean(p)] = true
		}
	}
	if p, err := readOptionalLink(c.cfg.CurrentLink); err == nil && p != "" {
		keep[filepath.Clean(p)] = true
	}
	if p, err := readOptionalLink(c.cfg.PreviousLink); err == nil && p != "" {
		keep[filepath.Clean(p)] = true
	}
	if p, err := readOptionalLink(c.cfg.UpdaterCurrentLink); err == nil && p != "" {
		keep[filepath.Dir(filepath.Clean(p))] = true
	}
	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && (lenPrefix(e.Name(), ".stage-") || lenPrefix(e.Name(), ".quarantine-")) {
			_ = os.RemoveAll(filepath.Join(c.cfg.VersionsDir, e.Name()))
			continue
		}
		if e.IsDir() && !keep[filepath.Join(c.cfg.VersionsDir, e.Name())] {
			dirs = append(dirs, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		a, _ := dirs[i].Info()
		b, _ := dirs[j].Info()
		return a.ModTime().After(b.ModTime())
	})
	budget := c.cfg.RetainVersions - len(keep)
	if budget < 0 {
		budget = 0
	}
	if budget > len(dirs) {
		budget = len(dirs)
	}
	for _, e := range dirs[budget:] {
		if safeComponent(e.Name()) {
			_ = os.RemoveAll(filepath.Join(c.cfg.VersionsDir, e.Name()))
		}
	}
}
func lenPrefix(v, p string) bool { return len(v) >= len(p) && v[:len(p)] == p }
