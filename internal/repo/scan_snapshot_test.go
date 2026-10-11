package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/scanner"
)

func scanFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitEnvironment(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newScanFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	scanFixtureGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("raw $Format:%H$\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("AGENTS.md export-ignore export-subst filter=canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scanFixtureGit(t, dir, "add", ".")
	scanFixtureGit(t, dir, "commit", "-m", "fixture")
	return dir, scanFixtureGit(t, dir, "rev-parse", "HEAD")
}

func TestScanAgentConfigPinsRawBranchAndCleansSnapshot(t *testing.T) {
	dir, sha := newScanFixture(t)
	root := t.TempDir()
	m := NewManager(root)
	m.AllowLocal = true
	got, inventory, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main")
	if err != nil || got != sha || len(inventory.Items) != 1 {
		t.Fatalf("got %q %#v %v", got, inventory, err)
	}
	digest := sha256.Sum256([]byte("raw $Format:%H$\n"))
	if inventory.Items[0].DigestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("raw blob changed: %#v", inventory)
	}
	entries, err := os.ReadDir(filepath.Join(root, "agent-config-scans"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshots retained: %v %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("new branch tip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scanFixtureGit(t, dir, "commit", "-am", "advance")
	newSHA := scanFixtureGit(t, dir, "rev-parse", "HEAD")
	got, _, err = m.ScanAgentConfig(context.Background(), dir, "refs/heads/main")
	if err != nil || got != newSHA || got == sha {
		t.Fatalf("stale branch: %s %v", got, err)
	}
	if _, _, err = m.ScanAgentConfig(context.Background(), dir, "refs/heads/missing"); !errors.Is(err, ErrScanUnavailable) {
		t.Fatalf("missing branch fell back: %v", err)
	}
}

func TestScanAgentConfigDoesNotExecuteGlobalTemplateOrFilters(t *testing.T) {
	dir, _ := newScanFixture(t)
	home := t.TempDir()
	canary := filepath.Join(home, "executed")
	template := filepath.Join(home, "templates", "hooks")
	if err := os.MkdirAll(template, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "post-checkout"), []byte("#!/bin/sh\ntouch '"+canary+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[init]\n templateDir = " + filepath.Dir(template) + "\n[filter \"canary\"]\n smudge = touch " + canary + "\n required = true\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", template)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	if _, _, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatal("Git executed configured hook/filter")
	}
}

func TestScanAgentConfigRejectsInvalidInputsAndCancellation(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, tc := range []struct{ url, ref string }{
		{"/tmp/local", "refs/heads/main"}, {"file:///tmp/local", "refs/heads/main"}, {"https://example.invalid/repo", "main"}, {"https://example.invalid/repo", "refs/tags/main"}, {"https://example.invalid/repo", "refs/heads/main\n"}, {"https://example.invalid/repo", "refs/heads/a..b"}, {"https://example.invalid/repo?token=x", "refs/heads/main"},
	} {
		if _, _, err := m.ScanAgentConfig(context.Background(), tc.url, tc.ref); !errors.Is(err, ErrScanInvalidInput) {
			t.Fatalf("accepted %+v: %v", tc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.ScanAgentConfig(ctx, "https://example.invalid/repo", "refs/heads/main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestScanAgentConfigFailsForGitlinkWithoutPartialInventory(t *testing.T) {
	dir, sha := newScanFixture(t)
	scanFixtureGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+",module")
	scanFixtureGit(t, dir, "commit", "-m", "gitlink")
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	got, inventory, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main")
	if !errors.Is(err, scanner.ErrIncompleteScan) || got != "" || len(inventory.Items) != 0 {
		t.Fatalf("partial result: %s %#v %v", got, inventory, err)
	}
}

func TestScanAgentConfigOriginMismatchAndMissingStorage(t *testing.T) {
	dir, _ := newScanFixture(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	scanFixtureGit(t, dir, "remote", "add", "origin", "https://example.invalid/unregistered")
	if got, inventory, err := m.scanPinnedOrigin(context.Background(), dir, "https://example.invalid/registered", "refs/heads/main", scanNetwork{}); !errors.Is(err, ErrScanUnavailable) || got != "" || len(inventory.Items) != 0 {
		t.Fatalf("origin mismatch accepted: %s %#v %v", got, inventory, err)
	}
	if _, _, err := m.scanPinnedOrigin(context.Background(), t.TempDir(), dir, "refs/heads/main", scanNetwork{}); !errors.Is(err, ErrScanUnavailable) {
		t.Fatalf("invalid mirror: %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.Root = file
	if _, _, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main"); !errors.Is(err, ErrScanUnavailable) {
		t.Fatalf("invalid storage: %v", err)
	}
}

func TestScanAgentConfigSupportsSHA256Origin(t *testing.T) {
	dir := t.TempDir()
	scanFixtureGit(t, dir, "init", "--object-format=sha256", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	scanFixtureGit(t, dir, "add", ".")
	scanFixtureGit(t, dir, "commit", "-m", "sha256 fixture")
	sha := scanFixtureGit(t, dir, "rev-parse", "HEAD")
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	got, inventory, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main")
	if err != nil || got != sha || len(got) != 64 || len(inventory.Items) != 1 {
		t.Fatalf("got %s %#v %v", got, inventory, err)
	}
}

func TestScanGitRawOutputBounds(t *testing.T) {
	m := NewManager(t.TempDir())
	out, err := m.scanGit(context.Background(), t.TempDir(), 256, "--version")
	if err != nil || !strings.HasSuffix(string(out), "\n") {
		t.Fatalf("raw output altered: %q %v", out, err)
	}
	if _, err := m.scanGit(context.Background(), t.TempDir(), 1, "--version"); !errors.Is(err, ErrScanLimit) {
		t.Fatalf("truncated success: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	output := scanOutput{limit: 2, cancel: cancel}
	if _, err := output.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("c")); !errors.Is(err, ErrScanLimit) || !output.overflow || ctx.Err() == nil {
		t.Fatalf("overflow not cancelled: %v", err)
	}
}

func TestScanGitCancellationKillsTransportChildren(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-completed")
	script := "#!/bin/sh\n(sleep 1; touch '" + marker + "') &\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	m := NewManager(t.TempDir())
	if _, err := m.scanGit(ctx, dir, 256, "--version"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("not cancelled: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("transport child survived cancellation")
	}
}

func TestScanGitSeparatesAndBoundsStderr(t *testing.T) {
	dir := t.TempDir()
	git := filepath.Join(dir, "git")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, script string
		wantLimit    bool
	}{
		{"diagnostics separated", "#!/bin/sh\nprintf 'raw\\n'\nprintf 'private diagnostic' >&2\n", false},
		{"diagnostics bounded", "#!/bin/sh\nprintf '" + strings.Repeat("x", (64<<10)+1) + "' >&2\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(git, []byte(tc.script), 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := NewManager(t.TempDir()).scanGit(context.Background(), dir, 256, "--version")
			if tc.wantLimit {
				if !errors.Is(err, ErrScanLimit) || len(out) != 0 {
					t.Fatalf("overflow result %q %v", out, err)
				}
			} else if err != nil || string(out) != "raw\n" {
				t.Fatalf("diagnostic mixed with stdout: %q %v", out, err)
			}
		})
	}
}

func TestScanGitDisablesLazyFetchPagerAndFilesystemMonitor(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$GIT_NO_LAZY_FETCH\" = 1 ] || exit 2\ncase \" $* \" in *' --no-pager '*) ;; *) exit 3;; esac\ncase \" $* \" in *' core.fsmonitor=false '*) ;; *) exit 4;; esac\nprintf 'safe\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := NewManager(t.TempDir()).scanGit(context.Background(), dir, 256, "--version")
	if err != nil || string(out) != "safe\n" {
		t.Fatalf("Git invocation lacked fixed defenses: %q %v", out, err)
	}
}

func TestAgentConfigScanSupportedOS(t *testing.T) {
	for platform, want := range map[string]bool{"linux": true, "darwin": true, "windows": false, "freebsd": false, "": false} {
		if got := AgentConfigScanSupportedOS(platform); got != want {
			t.Fatalf("platform %q supported=%t want=%t", platform, got, want)
		}
	}
}

func TestScanGitCancellationKillsChildrenAfterLeaderExits(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "orphan-completed")
	script := "#!/bin/sh\n(trap '' HUP; sleep 1; touch '" + marker + "') &\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = NewManager(t.TempDir()).scanGit(ctx, dir, 256, "--version")
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("transport child survived after Git leader exited")
	}
}

func TestScanProcessCancellationAfterLeaderReaped(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "orphan-completed")
	script := filepath.Join(dir, "transport")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n(trap '' HUP; sleep 1; touch '"+marker+"') </dev/null >/dev/null 2>&1 &\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), script)
	configureScanProcess(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Cancel()
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("transport child survived explicit cancellation after leader was reaped")
	}
}

func TestScanGitCleansTransportChildrenOnNormalExit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "orphan-completed")
	script := "#!/bin/sh\n(trap '' HUP; sleep 1; touch '" + marker + "') </dev/null >/dev/null 2>&1 &\nprintf 'raw\\n'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := NewManager(t.TempDir()).scanGit(context.Background(), dir, 256, "--version")
	if err != nil || string(out) != "raw\n" {
		t.Fatalf("unexpected Git result %q %v", out, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("normal Git exit stranded a transport child")
	}
}
