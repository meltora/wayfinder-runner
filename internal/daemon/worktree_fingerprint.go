package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errWorktreeChangedDuringComposition = errors.New("workspace changed during agent composition")

func worktreeFingerprint(ctx context.Context, dir string) (string, error) {
	digest := sha256.New()
	section := func(name string) { fmt.Fprintf(digest, "\x00section\x00%s\x00", name) }
	section("head")
	if err := fingerprintGit(ctx, dir, digest, "rev-parse", "HEAD"); err != nil {
		return "", err
	}
	section("status")
	if err := fingerprintGit(ctx, dir, digest, "status", "--porcelain=v2", "--untracked-files=all", "--ignored=matching", "-z"); err != nil {
		return "", err
	}
	section("tree")
	if err := fingerprintTree(dir, digest); err != nil {
		return "", err
	}
	var gitDir bytes.Buffer
	if err := fingerprintGit(ctx, dir, &gitDir, "rev-parse", "--absolute-git-dir"); err != nil {
		return "", err
	}
	for _, name := range []string{"info/exclude", "config", "HEAD"} {
		section(name)
		content, err := os.ReadFile(filepath.Join(strings.TrimSpace(gitDir.String()), filepath.FromSlash(name)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			digest.Write([]byte("\x00missing"))
		case err != nil:
			return "", fmt.Errorf("worktree fingerprint %s: %w", name, err)
		default:
			fmt.Fprintf(digest, "%d\x00", len(content))
			digest.Write(content)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func fingerprintGit(ctx context.Context, dir string, output io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = append(gitFingerprintEnvironment(),
		"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	)
	command.Stdout = output
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("worktree fingerprint git %s: %w", args[0], err)
	}
	return nil
}

func gitFingerprintEnvironment() []string {
	var environment []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func fingerprintTree(root string, digest hash.Hash) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(digest, "%s\x00%s\x00%d\x00", rel, info.Mode().String(), info.Size())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(digest, "link\x00%s\x00", target)
		case info.Mode().IsRegular():
			content := sha256.New()
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(content, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			fmt.Fprintf(digest, "file\x00%x\x00", content.Sum(nil))
		default:
			digest.Write([]byte("other\x00"))
		}
		return nil
	})
}
