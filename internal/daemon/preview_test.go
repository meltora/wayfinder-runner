package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func previewFixture(t *testing.T) (*jobRunner, *capturedEvents, map[string]string) {
	t.Helper()
	jr, captured := testJobRunner(t)
	want := make(map[string]string, 2)
	for index, repositoryID := range []string{"primary", "secondary"} {
		workDir := workspaceRepositoryDir(t, jr, repositoryID)
		initRunCheckRepo(t, workDir)
		jr.repositories = append(jr.repositories, checkedOutRepository{
			ID: repositoryID, WorkDir: workDir, Primary: index == 0,
		})
		want[repositoryID] = gitHead(t, workDir)
	}
	attachTrustedRepositories(t, jr)
	return jr, captured, want
}

func previewArguments(t *testing.T, fields map[string]any) string {
	t.Helper()
	if _, ok := fields["url"]; !ok {
		fields["url"] = "https://preview.example.test/feature"
	}
	if _, ok := fields["health"]; !ok {
		fields["health"] = "healthy"
	}
	if _, ok := fields["expires_at"]; !ok {
		fields["expires_at"] = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPublishPreviewDerivesExactCleanRepositoryCommitSet(t *testing.T) {
	jr, captured, want := previewFixture(t)
	args := previewArguments(t, map[string]any{
		"known_limitations": []string{"Email delivery is disabled"},
		"idempotency_key":   "preview-clean",
	})
	if _, err := call(t, jr, "platform_publish_preview", args); err != nil {
		t.Fatal(err)
	}
	events := captured.byKind("preview")
	if len(events) != 1 {
		t.Fatalf("preview events = %v", events)
	}
	event := events[0]
	commitSet, ok := event["commit_set"].(map[string]string)
	if !ok {
		t.Fatalf("commit_set type = %T", event["commit_set"])
	}
	if len(commitSet) != len(want) {
		t.Fatalf("commit_set = %v, want %v", commitSet, want)
	}
	for repositoryID, commitSHA := range want {
		if commitSet[repositoryID] != commitSHA {
			t.Fatalf("commit_set[%q] = %q, want %q", repositoryID, commitSet[repositoryID], commitSHA)
		}
	}
	for _, forbidden := range []string{"commit_sha", "idempotency_key"} {
		if _, exists := event[forbidden]; exists {
			t.Fatalf("preview event leaked %s: %v", forbidden, event)
		}
	}
	limitations, ok := event["known_limitations"].([]string)
	if !ok || len(limitations) != 1 {
		t.Fatalf("known_limitations = %#v", event["known_limitations"])
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, event["expires_at"].(string))
	if err != nil || expiresAt.Location() != time.UTC {
		t.Fatalf("expires_at = %v, err = %v", event["expires_at"], err)
	}
}

func TestPublishPreviewOfADirtyRepositoryReportsTheResultCommit(t *testing.T) {
	jr, captured, want := previewFixture(t)
	if err := os.WriteFile(filepath.Join(jr.repositories[1].WorkDir, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := previewArguments(t, map[string]any{"idempotency_key": "preview-dirty"})
	if _, err := call(t, jr, "platform_publish_preview", args); err != nil {
		t.Fatalf("dirty preview error = %v", err)
	}
	events := captured.byKind("preview")
	if len(events) != 1 {
		t.Fatalf("preview events = %v", events)
	}
	commitSet := events[0]["commit_set"].(map[string]string)
	if commitSet["primary"] != want["primary"] || commitSet["secondary"] == want["secondary"] {
		t.Fatalf("commit_set = %v, clean heads %v", commitSet, want)
	}
	paths := trustedGitForTest(t, jr.repositories[1].Trusted.gitDir, "ls-tree", "-r", "--name-only", commitSet["secondary"])
	if !strings.Contains(paths, "dirty.txt") {
		t.Fatalf("preview commit does not hold the edit: %s", paths)
	}
}

func TestPublishPreviewRefusesAnAmbiguousResult(t *testing.T) {
	jr, captured, _ := previewFixture(t)
	checkout := jr.repositories[0].WorkDir
	worktree := filepath.Join(jr.workDir, "agent-side")
	agentGit(t, checkout, "worktree", "add", "-q", "-b", "agent-side", worktree)
	writeAgentFile(t, worktree, "side.txt", "side\n")
	writeAgentFile(t, checkout, "main.txt", "main\n")
	args := previewArguments(t, map[string]any{"idempotency_key": "preview-ambiguous"})
	if _, err := call(t, jr, "platform_publish_preview", args); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous preview error = %v", err)
	}
	if events := captured.byKind("preview"); len(events) != 0 {
		t.Fatalf("ambiguous preview emitted evidence: %v", events)
	}
}

func TestPublishPreviewRejectsUntrustedOrUnboundedFields(t *testing.T) {
	jr, _, _ := previewFixture(t)
	tooManyLimitations := make([]string, 129)
	for index := range tooManyLimitations {
		tooManyLimitations[index] = fmt.Sprintf("limitation-%d", index)
	}
	cases := []map[string]any{
		{"commit_sha": strings.Repeat("a", 40)},
		{"commit_set": map[string]any{"primary": strings.Repeat("a", 40)}},
		{"unexpected": true},
		{"url": "https://preview.example.test/feature?token=secret"},
		{"url": "https://user:secret@preview.example.test/feature"},
		{"url": "https://preview.example.test/feature#fragment"},
		{"url": "https://preview.example.test\\@evil.test/feature"},
		{"health": "unknown"},
		{"expires_at": "not-a-time"},
		{"expires_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)},
		{"expires_at": time.Now().UTC().Add(30*24*time.Hour + time.Minute).Format(time.RFC3339Nano)},
		{"known_limitations": "none"},
		{"known_limitations": []any{"works", 7}},
		{"known_limitations": []string{"   "}},
		{"known_limitations": []string{"unsafe\u0001text"}},
		{"known_limitations": []string{strings.Repeat("x", 4097)}},
		{"known_limitations": tooManyLimitations},
	}
	for index, fields := range cases {
		fields["idempotency_key"] = fmt.Sprintf("preview-invalid-%d", index)
		args := previewArguments(t, fields)
		if _, err := call(t, jr, "platform_publish_preview", args); err == nil {
			t.Fatalf("invalid preview accepted: %s", args)
		}
	}
}

func TestPreviewCommitSetRequiresAuthoritativeRepositoryInventory(t *testing.T) {
	jr, _, _ := previewFixture(t)
	jr.repositories = nil
	args := previewArguments(t, map[string]any{"idempotency_key": "preview-no-repositories"})
	if _, err := call(t, jr, "platform_publish_preview", args); err == nil {
		t.Fatal("preview accepted an empty repository inventory")
	}
}
