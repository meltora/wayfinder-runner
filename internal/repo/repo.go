package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/validation"
)

type Manager struct {
	Root       string
	AllowLocal bool
	Git        func(ctx context.Context, dir string, args ...string) (string, error)
}

func NewManager(root string) *Manager {
	m := &Manager{Root: root}
	m.Git = m.runGit
	return m
}

func (m *Manager) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return m.runGitWithProtocols(ctx, dir, m.AllowLocal, args...)
}

func (m *Manager) runGitWithProtocols(ctx context.Context, dir string, allowFile bool, args ...string) (string, error) {
	// #nosec G204 -- executable is fixed; refs/URLs are validated and argv is never interpreted by a shell.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	protocols := "https:ssh"
	if allowFile {
		protocols += ":file"
	}
	cmd.Env = append(gitEnvironment(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_ALLOW_PROTOCOL="+protocols,
	)
	var out limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}

type limitedOutput struct {
	data      []byte
	truncated bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	const limit = 1 << 20
	n := len(p)
	remaining := limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		b.data = append(b.data, p...)
	} else {
		b.truncated = true
	}
	return n, nil
}

func (b *limitedOutput) String() string {
	if b.truncated {
		return string(b.data) + "\n[git output truncated]"
	}
	return string(b.data)
}

// CreateWorkspace creates a self-contained clone inside a job workspace. It
// deliberately avoids linked worktrees and shared objects so the agent never
// needs access to the runner-owned mirror metadata.
func (m *Manager) CreateWorkspace(ctx context.Context, url, branch, baseRef, dest string) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitRef("branch", branch); err != nil {
		return err
	}
	if err := validation.GitRef("base_ref", baseRef); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("workspace destination already exists")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	mirror := m.mirrorDir(url)
	if _, err := m.runGitWithProtocols(ctx, "", true, "clone", "--no-hardlinks", "--no-checkout", mirror, dest); err != nil {
		return err
	}
	resolvedBase, err := m.resolveWorkspaceBase(ctx, dest, baseRef)
	if err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	if _, err := m.runGitWithProtocols(ctx, dest, true, "checkout", "-b", branch, resolvedBase); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	return nil
}

func (m *Manager) resolveWorkspaceBase(ctx context.Context, dest, baseRef string) (string, error) {
	candidates := []string{baseRef}
	if !strings.HasPrefix(baseRef, "refs/remotes/") {
		branch := strings.TrimPrefix(baseRef, "refs/heads/")
		candidates = append(candidates, "refs/remotes/origin/"+branch)
	}
	for _, candidate := range candidates {
		if _, err := m.runGitWithProtocols(
			ctx,
			dest,
			true,
			"rev-parse",
			"--verify",
			candidate+"^{commit}",
		); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("workspace base ref %q does not resolve to a commit", baseRef)
}

func (m *Manager) RemoveWorkspace(dest string) error {
	if dest == "" || !filepath.IsAbs(dest) {
		return fmt.Errorf("workspace path must be absolute")
	}
	return os.RemoveAll(dest)
}

// ImportBundle imports a bundle produced inside the untrusted job container
// into the runner-owned mirror, then verifies the exact commit selected.
func (m *Manager) ImportBundle(ctx context.Context, url, bundlePath, branch, expectedSHA string) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitRef("branch", branch); err != nil {
		return err
	}
	if !validObjectID(expectedSHA) {
		return fmt.Errorf("expected commit is not a Git object ID")
	}
	info, err := os.Lstat(bundlePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 512<<20 {
		return fmt.Errorf("git bundle must be a regular file between 1 byte and 512 MiB")
	}
	mirror := m.mirrorDir(url)
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	if _, err := m.runGitWithProtocols(ctx, mirror, true, "fetch", "--no-tags", "--force", bundlePath, refspec); err != nil {
		return err
	}
	got, err := m.runGitWithProtocols(ctx, mirror, false, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return err
	}
	if got != expectedSHA {
		return fmt.Errorf("imported commit mismatch: got %s want %s", got, expectedSHA)
	}
	return nil
}

func (m *Manager) PushMirrorBranch(ctx context.Context, url, branch string) error {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return err
	}
	if err := validation.GitRef("branch", branch); err != nil {
		return err
	}
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	_, err := m.runGitWithProtocols(ctx, m.mirrorDir(url), m.AllowLocal,
		"-c", "core.hooksPath=/dev/null", "-c", "remote.origin.mirror=false", "push", "origin", refspec)
	return err
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func (m *Manager) mirrorDir(url string) string {
	digest := sha256.Sum256([]byte(url))
	return filepath.Join(m.Root, "mirrors", "repo-"+hex.EncodeToString(digest[:16])+".git")
}

func (m *Manager) Register(ctx context.Context, url string) (string, error) {
	if err := validateURL(url, m.AllowLocal); err != nil {
		return "", err
	}
	dir := m.mirrorDir(url)
	if _, err := os.Stat(dir); err == nil {
		origin, err := m.Git(ctx, dir, "remote", "get-url", "origin")
		if err != nil {
			return "", err
		}
		if origin != url {
			return "", fmt.Errorf("repository mirror origin mismatch: got %q", origin)
		}
		if _, err := m.Git(ctx, dir, "worktree", "prune"); err != nil {
			return "", err
		}
		if _, err := m.Git(ctx, dir, "fetch", "--prune", "origin"); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	if _, err := m.Git(ctx, "", "clone", "--mirror", url, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func validateURL(raw string, allowLocal bool) error {
	if filepath.IsAbs(raw) {
		if allowLocal {
			return nil
		}
		return fmt.Errorf("local repositories are disabled")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return fmt.Errorf("repository URL must use HTTPS or SSH")
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("repository URL must not contain a query or fragment")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return fmt.Errorf("repository URL must not embed credentials")
		}
	}
	return nil
}

func gitEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TMPDIR": true, "LANG": true, "LC_ALL": true,
		"SSH_AUTH_SOCK": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
	}
	out := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}

func (m *Manager) DefaultBranch(ctx context.Context, url string) (string, error) {
	out, err := m.Git(ctx, m.mirrorDir(url), "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(out, "refs/heads/"), nil
}

func (m *Manager) CreateWorktree(ctx context.Context, url, branch, baseRef, dest string) error {
	if err := validation.GitRef("branch", branch); err != nil {
		return err
	}
	if err := validation.GitRef("base_ref", baseRef); err != nil {
		return err
	}
	mirror := m.mirrorDir(url)
	if _, err := m.Git(ctx, mirror, "worktree", "add", "-b", branch, dest, baseRef); err != nil {
		return err
	}
	return nil
}

func (m *Manager) RemoveWorktree(ctx context.Context, url, dest string) error {
	mirror := m.mirrorDir(url)
	if _, err := m.Git(ctx, mirror, "worktree", "remove", "--force", dest); err != nil {
		if rmErr := os.RemoveAll(dest); rmErr != nil {
			return fmt.Errorf("%v; fallback remove: %w", err, rmErr)
		}
	}
	return nil
}

func (m *Manager) HeadSHA(ctx context.Context, dir string) (string, error) {
	return m.Git(ctx, dir, "rev-parse", "HEAD")
}

func (m *Manager) Diff(ctx context.Context, dir, base string) (string, error) {
	if err := validation.GitRef("base_ref", base); err != nil {
		return "", err
	}
	return m.Git(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", base+"...HEAD")
}
