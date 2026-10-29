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
	if err := fingerprintGit(exec.CommandContext(ctx, "git", "rev-parse", "HEAD"), dir, digest); err != nil {
		return "", err
	}
	section("status")
	if err := fingerprintGit(exec.CommandContext(ctx, "git", "status", "--porcelain=v2", "--untracked-files=all", "--ignored=matching", "-z"), dir, digest); err != nil {
		return "", err
	}
	section("tree")
	if err := fingerprintTree(dir, digest); err != nil {
		return "", err
	}
	var gitDir bytes.Buffer
	if err := fingerprintGit(exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir"), dir, &gitDir); err != nil {
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

func fingerprintGit(command *exec.Cmd, dir string, output io.Writer) error {
	command.Dir = dir
	command.Env = append(gitFingerprintEnvironment(),
		"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	)
	command.Stdout = output
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("worktree fingerprint git %s: %w", command.Args[1], err)
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

func fingerprintTree(dir string, digest hash.Hash) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		fmt.Fprintf(digest, "%s\x00%s\x00%d\x00", rel, info.Mode().String(), info.Size())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(rel)
			if err != nil {
				return err
			}
			fmt.Fprintf(digest, "link\x00%s\x00", target)
		case info.Mode().IsRegular():
			sum, err := fingerprintRegularFile(root, rel, info)
			if err != nil {
				return err
			}
			fmt.Fprintf(digest, "file\x00%x\x00", sum)
		default:
			digest.Write([]byte("other\x00"))
		}
		return nil
	})
}

func fingerprintRegularFile(root *os.Root, rel string, listed fs.FileInfo) ([]byte, error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(listed, opened) {
		return nil, fmt.Errorf("worktree fingerprint %s: %w", rel, errWorktreeChangedDuringComposition)
	}
	content := sha256.New()
	if _, err := io.Copy(content, file); err != nil {
		return nil, err
	}
	return content.Sum(nil), nil
}
