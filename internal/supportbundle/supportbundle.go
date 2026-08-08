package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/safeio"
)

var allowedFiles = map[string]bool{
	"runner-id": true,
	"paused":    true,
}

func Create(stateDir string) (string, error) {
	out, err := os.CreateTemp("", "our-runner-support-*.tar.gz")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	if err := out.Chmod(0o600); err != nil {
		_ = out.Close()
		_ = os.Remove(outPath)
		return "", err
	}
	defer func() { _ = out.Close() }()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	err = filepath.WalkDir(stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stateDir, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !allowedFiles[rel] {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}
		f, err := safeio.OpenRegular(stateDir, rel)
		if err != nil {
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o600, Size: info.Size()}); err != nil {
			_ = f.Close()
			return err
		}
		_, err = io.CopyN(tw, f, info.Size())
		_ = f.Close()
		return err
	})
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("sync support bundle: %w", err)
	}
	return outPath, nil
}
