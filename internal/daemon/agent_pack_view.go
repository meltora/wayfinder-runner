package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/example/app-runner/internal/safeio"
)

const (
	agentPackViewFileMode      fs.FileMode = 0o444
	agentPackViewDirectoryMode fs.FileMode = 0o555
)

func verifyAgentPackView(root string, files map[string]string) error {
	if len(files) == 0 {
		return fmt.Errorf("Agent Pack view has no authenticated manifest")
	}
	directories := map[string]bool{".": true}
	for name := range files {
		for current := path.Dir(name); current != "."; current = path.Dir(current) {
			directories[current] = true
		}
	}
	view, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = view.Close() }()
	seen := make(map[string]bool, len(files))
	err = fs.WalkDir(view.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := view.Lstat(rel)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if !directories[rel] {
				return fmt.Errorf("Agent Pack view has an unexpected directory %q", rel)
			}
			if info.Mode().Perm() != agentPackViewDirectoryMode {
				return fmt.Errorf("Agent Pack view directory %q has mode %v", rel, info.Mode().Perm())
			}
		case info.Mode().IsRegular():
			want, ok := files[rel]
			if !ok {
				return fmt.Errorf("Agent Pack view has an unexpected file %q", rel)
			}
			if info.Mode().Perm() != agentPackViewFileMode {
				return fmt.Errorf("Agent Pack view file %q has mode %v", rel, info.Mode().Perm())
			}
			got, err := agentPackViewFileDigest(root, rel)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("Agent Pack view file %q does not match its authenticated digest", rel)
			}
			seen[rel] = true
		default:
			return fmt.Errorf("Agent Pack view entry %q is not a regular file or directory", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(files) {
		return fmt.Errorf("Agent Pack view is missing %d authenticated files", len(files)-len(seen))
	}
	return nil
}

func agentPackViewFileDigest(root, rel string) (string, error) {
	file, err := safeio.OpenRegular(root, rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
