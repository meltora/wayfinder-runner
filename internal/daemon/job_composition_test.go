package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/supervisor"
)

type compositionTestProcess struct{}

func (compositionTestProcess) PID() int         { return 4242 }
func (compositionTestProcess) Wait() error      { return nil }
func (compositionTestProcess) Interrupt() error { return nil }
func (compositionTestProcess) Kill() error      { return nil }

type compositionTestSupervisor struct {
	mu        sync.Mutex
	starts    int
	spec      supervisor.Spec
	duringRun func(supervisor.Spec)
}

func (*compositionTestSupervisor) Name() string    { return "composition-test" }
func (*compositionTestSupervisor) Available() bool { return true }
func (value *compositionTestSupervisor) Start(_ context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	value.mu.Lock()
	value.starts++
	value.spec = spec
	value.mu.Unlock()
	if value.duringRun != nil {
		value.duringRun(spec)
	}
	return compositionTestProcess{}, nil
}

type compositionRun struct {
	jr         *jobRunner
	events     *capturedEvents
	supervisor *compositionTestSupervisor
	checkout   string
}

func compositionPackServer(t *testing.T) (v1.AgentPackRef, *http.Client) {
	t.Helper()
	var archive bytes.Buffer
	if _, err := agentpack.WriteCanonicalArchive(&archive, agentpack.ArchiveContent{
		PackKey: "platform", Provider: "claude-code", Version: "1.0.0",
		Files: map[string][]byte{
			".claude-plugin/plugin.json": []byte(`{"name":"shortstack-platform"}`),
			"skills/x/SKILL.md":          []byte("# pack skill x"),
			"WORKFLOW.md":                []byte("# workflow"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	raw := archive.Bytes()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(raw)
	}))
	t.Cleanup(server.Close)
	sum := sha256.Sum256(raw)
	return v1.AgentPackRef{Version: "1.0.0", Digest: hex.EncodeToString(sum[:]), KeyID: "platform-key-1", URL: server.URL + "/pack.tgz"}, server.Client()
}

func newCompositionRun(t *testing.T, withPack bool) *compositionRun {
	t.Helper()
	return newCompositionRunWithFiles(t, withPack, nil)
}

func newCompositionRunWithFiles(t *testing.T, withPack bool, files map[string]string) *compositionRun {
	t.Helper()
	origin := t.TempDir()
	buildWorkspaceGit(t, origin, "init", "-b", "main")
	buildWorkspaceGit(t, origin, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, origin, "config", "user.name", "Runner Test")
	writeFixtureFile(t, origin, "CLAUDE.md", "repository instructions\n", 0o644)
	writeFixtureFile(t, origin, ".claude/skills/x/SKILL.md", "# repository skill x\n", 0o644)
	writeFixtureFile(t, origin, "services/api/CLAUDE.md", "nested instructions\n", 0o644)
	writeFixtureFile(t, origin, "main.go", "package main\n", 0o644)
	for name, content := range files {
		writeFixtureFile(t, origin, name, content, 0o644)
	}
	buildWorkspaceGit(t, origin, "add", "-A")
	buildWorkspaceGit(t, origin, "commit", "-m", "fixture")
	baseSHA := buildWorkspaceGit(t, origin, "rev-parse", "HEAD")

	jr, events := testJobRunner(t)
	sup := &compositionTestSupervisor{}
	jr.d.supervisor = sup
	jr.d.cfg.Provider = "claude"
	jr.d.cfg.Isolation = "direct"
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	jr.jobToken = "composition-test-token"
	jr.job.Kind = v1.JobKindPlan
	jr.job.Ticket = v1.TicketContext{Key: "PROJ-1", Summary: "Compose"}
	jr.job.Repositories = []v1.JobRepository{{RepositoryID: "repo-1", BaseBranch: "main", BaseSHA: baseSHA, Primary: true}}
	if withPack {
		reference, client := compositionPackServer(t)
		jr.job.AgentPack = &reference
		jr.d.cfg.ControlPlaneHTTPURL = strings.TrimSuffix(reference.URL, "/pack.tgz")
		jr.d.cfg.AllowedArtifactOrigins = []string{jr.d.cfg.ControlPlaneHTTPURL}
		jr.d.controlHTTP = client
	}
	return &compositionRun{jr: jr, events: events, supervisor: sup, checkout: filepath.Join(jr.workDir, "repositories", "repo-1")}
}

func (run *compositionRun) execute(t *testing.T) {
	t.Helper()
	run.jr.run()
}

func (run *compositionRun) finished(t *testing.T) map[string]any {
	t.Helper()
	finished := run.events.byKind("job_finished")
	if len(finished) != 1 {
		t.Fatalf("job_finished events = %d", len(finished))
	}
	return finished[0]
}

func (run *compositionRun) compositions() []map[string]any {
	return run.events.byKind(v1.EventKindAgentPackComposition)
}

func settingsDirectory(t *testing.T, args []string) string {
	t.Helper()
	for index, argument := range args {
		if argument == "--settings" && index+1 < len(args) {
			return filepath.Dir(args[index+1])
		}
	}
	t.Fatalf("args lack --settings: %v", args)
	return ""
}

func argumentFollowing(args []string, flag string) string {
	for index, argument := range args {
		if argument == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func TestCompositionAttemptLeavesTheWorktreeUntouchedAndReportsBothPhases(t *testing.T) {
	run := newCompositionRun(t, true)
	var duringRunFingerprint, recordedFingerprint string
	var runnerFiles []string
	var prompt []byte
	run.supervisor.duringRun = func(spec supervisor.Spec) {
		var err error
		duringRunFingerprint, err = worktreeFingerprint(context.Background(), run.checkout)
		if err != nil {
			t.Error(err)
		}
		if prompt, err = os.ReadFile(spec.StdinFile); err != nil {
			t.Error(err)
		}
		recordedFingerprint = run.jr.repositories[0].Fingerprint
		_ = filepath.WalkDir(filepath.Join(run.jr.workDir, "repositories"), func(path string, entry fs.DirEntry, err error) error {
			if err == nil && (entry.Name() == "claude-settings.json" || entry.Name() == "claude-mcp.json" ||
				entry.Name() == "job.token" || strings.HasPrefix(entry.Name(), "managed-hook-")) {
				runnerFiles = append(runnerFiles, path)
			}
			return nil
		})
	}
	run.execute(t)

	if run.supervisor.starts != 1 {
		t.Fatalf("starts = %d", run.supervisor.starts)
	}
	if duringRunFingerprint == "" || duringRunFingerprint != recordedFingerprint {
		t.Fatalf("fingerprint after composition %q != checkout fingerprint %q", duringRunFingerprint, recordedFingerprint)
	}
	if len(runnerFiles) != 0 {
		t.Fatalf("runner files under the checkout: %v", runnerFiles)
	}
	args := run.supervisor.spec.Args
	packDir := filepath.Join(run.jr.workDir, "pack")
	if argumentFollowing(args, "--plugin-dir") != packDir || argumentFollowing(args, "--add-dir") != packDir {
		t.Fatalf("pack args = %v", args)
	}
	configDir := settingsDirectory(t, args)
	if strings.HasPrefix(configDir, run.jr.workDir) {
		t.Fatalf("ConfigDir %s is inside the job root", configDir)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("ConfigDir survived the attempt: %v", err)
	}
	if _, err := os.Stat(run.jr.workDir); !os.IsNotExist(err) {
		t.Fatalf("job root survived the attempt: %v", err)
	}

	reports := run.compositions()
	if len(reports) != 2 || reports[0]["phase"] != "before_start" || reports[1]["phase"] != "after_exit" {
		t.Fatalf("composition reports = %v", reports)
	}
	for _, report := range reports {
		pack := report["agent_pack"].(map[string]any)
		if report["pack_view"] != "verified" || pack["plugin_loaded"] != true || pack["key_id"] != "platform-key-1" ||
			report["isolation"] != "direct" || report["adapter"] != "claude" {
			t.Fatalf("report = %v", report)
		}
		conflicts := report["conflicts"].([]any)
		found := false
		for _, raw := range conflicts {
			conflict := raw.(map[string]any)
			found = found || (conflict["code"] == "skill_name_collision" && conflict["path"] == ".claude/skills/x")
		}
		if !found {
			t.Fatalf("collision missing: %v", conflicts)
		}
	}
	finishedIndex, beforeIndex, afterIndex := -1, -1, -1
	for index, event := range run.events.events {
		switch {
		case event["kind"] == "job_finished":
			finishedIndex = index
		case event["kind"] == v1.EventKindAgentPackComposition && event["phase"] == "before_start":
			beforeIndex = index
		case event["kind"] == v1.EventKindAgentPackComposition && event["phase"] == "after_exit":
			afterIndex = index
		}
	}
	if !(beforeIndex < afterIndex && afterIndex < finishedIndex) {
		t.Fatalf("event order before=%d after=%d finished=%d", beforeIndex, afterIndex, finishedIndex)
	}
	if !strings.Contains(string(prompt), "verified, permission-restricted directory") {
		t.Fatalf("direct prompt claims a read-only mount: %s", prompt)
	}
}

func assertCompositionStartRefused(t *testing.T, run *compositionRun, code, category, message string) {
	t.Helper()
	if run.supervisor.starts != 0 {
		t.Fatalf("provider started %d times", run.supervisor.starts)
	}
	finished := run.finished(t)
	if finished["state"] != "failed" || finished["failure_code"] != code {
		t.Fatalf("job_finished = %v", finished)
	}
	if got, _ := finished["failure_category"].(string); got != category {
		t.Fatalf("failure_category = %v", finished["failure_category"])
	}
	if message != "" && finished["failure_message"] != message {
		t.Fatalf("failure_message = %v", finished["failure_message"])
	}
	if len(run.compositions()) != 0 {
		t.Fatalf("composition reported for a refused start: %v", run.compositions())
	}
	if _, err := os.Stat(run.jr.workDir); !os.IsNotExist(err) {
		t.Fatalf("job root survived: %v", err)
	}
	if run.jr.configDir == "" {
		t.Fatal("ConfigDir was never created")
	}
	if _, err := os.Stat(run.jr.configDir); !os.IsNotExist(err) {
		t.Fatalf("ConfigDir survived: %v", err)
	}
}

func TestCompositionStepWritingIntoTheCheckoutFailsBeforeTheProviderStarts(t *testing.T) {
	run := newCompositionRun(t, true)
	run.jr.compositionHook = func(context.Context) error {
		return os.WriteFile(filepath.Join(run.checkout, ".claude", "settings.local.json"), []byte("{}"), 0o644)
	}
	run.execute(t)
	assertCompositionStartRefused(t, run, string(v1.JobFailureWorkspacePreparation), "", "workspace changed during agent composition")
}

func TestResumedAttemptRecomputesBothFingerprints(t *testing.T) {
	tampered := newCompositionRun(t, false)
	tampered.jr.job.ResumeSessionID = "session-1"
	tampered.jr.compositionHook = func(context.Context) error {
		return os.Chmod(filepath.Join(tampered.checkout, "main.go"), 0o755)
	}
	tampered.execute(t)
	assertCompositionStartRefused(t, tampered, string(v1.JobFailureWorkspacePreparation), "", "workspace changed during agent composition")

	resumed := newCompositionRun(t, false)
	resumed.jr.job.ResumeSessionID = "session-1"
	var recorded string
	resumed.supervisor.duringRun = func(supervisor.Spec) { recorded = resumed.jr.repositories[0].Fingerprint }
	resumed.execute(t)
	if resumed.supervisor.starts != 1 || argumentFollowing(resumed.supervisor.spec.Args, "--resume") != "session-1" {
		t.Fatalf("resumed start = %d args = %v", resumed.supervisor.starts, resumed.supervisor.spec.Args)
	}
	if recorded == "" {
		t.Fatal("resumed attempt has no fingerprint of its own checkout")
	}
	reports := resumed.compositions()
	if len(reports) != 2 || reports[0]["pack_view"] != "absent" || reports[0]["agent_pack"] != nil {
		t.Fatalf("reports without a pack = %v", reports)
	}
}

func TestCompositionHookFailureStartsNoProvider(t *testing.T) {
	run := newCompositionRun(t, false)
	run.jr.compositionHook = func(context.Context) error { return errors.New("composition failed") }
	run.execute(t)
	assertCompositionStartRefused(t, run, string(v1.JobFailureRunnerInternal), "", "")
}

func TestPackViewTamperedBeforeStartFailsWithoutAProvider(t *testing.T) {
	run := newCompositionRun(t, true)
	run.jr.compositionHook = func(context.Context) error {
		return os.Chmod(filepath.Join(run.jr.workDir, "pack", "WORKFLOW.md"), 0o644)
	}
	run.execute(t)
	assertCompositionStartRefused(t, run, string(v1.JobFailureAgentPackMaterialization), string(v1.AgentPackFailureArchiveInvalid), "")
	if retryable, ok := run.finished(t)["retryable"].(bool); !ok || retryable {
		t.Fatalf("retryable = %v", run.finished(t)["retryable"])
	}
}

func TestPackViewTamperedDuringTheRunIsReportedWithoutChangingTheOutcome(t *testing.T) {
	untouched := newCompositionRun(t, true)
	untouched.execute(t)

	tampered := newCompositionRun(t, true)
	tampered.supervisor.duringRun = func(supervisor.Spec) {
		if err := os.Chmod(filepath.Join(tampered.jr.workDir, "pack", "WORKFLOW.md"), 0o644); err != nil {
			t.Error(err)
		}
	}
	tampered.execute(t)

	reports := tampered.compositions()
	if len(reports) != 2 || reports[0]["pack_view"] != "verified" || reports[1]["pack_view"] != "modified" {
		t.Fatalf("tampered reports = %v", reports)
	}
	if got := untouched.compositions(); len(got) != 2 || got[1]["pack_view"] != "verified" {
		t.Fatalf("untouched reports = %v", got)
	}
	want, got := untouched.finished(t), tampered.finished(t)
	if want["state"] != got["state"] || want["failure_code"] != got["failure_code"] || want["failure_category"] != got["failure_category"] {
		t.Fatalf("outcome changed: untouched=%v tampered=%v", want, got)
	}
}

func TestPromptDescribesThePackViewForEachIsolation(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.packDir = "/jobs/job-1/pack"
	jr.d.cfg.Isolation = "docker"
	if prompt := jr.buildPrompt(); !strings.Contains(prompt, "mounted read-only at /jobs/job-1/pack") || strings.Contains(prompt, "permission-restricted") {
		t.Fatalf("docker prompt = %s", prompt)
	}
	jr.d.cfg.Isolation = "direct"
	prompt := jr.buildPrompt()
	if strings.Contains(prompt, "read-only") || !strings.Contains(prompt, "provided as a verified, permission-restricted directory at /jobs/job-1/pack") {
		t.Fatalf("direct prompt = %s", prompt)
	}
}

var repositoryConfigurationConflictFiles = map[string]string{
	".claude/settings.json": "{\"permissions\":{\"allow\":[\"Bash\"]}}\n",
	".mcp.json":             "{\"mcpServers\":{}}\n",
}

func TestRepositorySettingsAndMCPInTheCheckoutNeverReachAProviderStart(t *testing.T) {
	run := newCompositionRunWithFiles(t, true, repositoryConfigurationConflictFiles)
	run.execute(t)
	if run.supervisor.starts != 0 {
		t.Fatalf("provider started %d times", run.supervisor.starts)
	}
	finished := run.finished(t)
	if finished["state"] != "failed" {
		t.Fatalf("job_finished = %v", finished)
	}
	if len(run.compositions()) != 0 {
		t.Fatalf("composition claimed repository settings or MCP before approval: %v", run.compositions())
	}
	discoveries := run.events.byKind("agent_config_discovery")
	if len(discoveries) != 1 {
		t.Fatalf("discovery events = %d", len(discoveries))
	}
	items, _ := discoveries[0]["items"].([]any)
	seen := map[string]bool{}
	for _, raw := range items {
		item := raw.(map[string]any)
		seen[item["path"].(string)] = item["decision"] == "pending"
	}
	for name := range repositoryConfigurationConflictFiles {
		if !seen[name] {
			t.Fatalf("discovery lacks pending %s: %v", name, items)
		}
	}
	if _, err := os.Stat(run.jr.workDir); !os.IsNotExist(err) {
		t.Fatalf("job root survived: %v", err)
	}
}

func TestCompositionReportsRepositorySettingsAndMCPConflictsFromARealCheckout(t *testing.T) {
	run := newCompositionRunWithFiles(t, false, repositoryConfigurationConflictFiles)
	origin := run.jr.d.cfg.Repositories["repo-1"].URL
	checkout := filepath.Join(t.TempDir(), "repo-1")
	buildWorkspaceGit(t, filepath.Dir(checkout), "clone", "-q", origin, checkout)
	fingerprint, err := worktreeFingerprint(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	run.jr.repositories = []checkedOutRepository{{ID: "repo-1", URL: origin, WorkDir: checkout, Primary: true, Fingerprint: fingerprint}}
	run.jr.configDir = t.TempDir()
	if outcome, refused := run.jr.verifyCompositionBeforeStart(context.Background(), compositionStartContext{
		adapter: "claude", isolation: v1.AgentPackCompositionIsolationDirect,
	}); refused {
		t.Fatalf("composition refused: %+v", outcome)
	}
	reports := run.compositions()
	if len(reports) != 1 || reports[0]["phase"] != "before_start" {
		t.Fatalf("reports = %v", reports)
	}
	want := map[string]string{
		".claude/settings.json": "repository_settings_excluded",
		".mcp.json":             "repository_mcp_excluded",
	}
	got := map[string]map[string]any{}
	for _, raw := range reports[0]["conflicts"].([]any) {
		conflict := raw.(map[string]any)
		if path, ok := conflict["path"].(string); ok {
			got[path] = conflict
		}
	}
	for path, code := range want {
		conflict := got[path]
		if conflict == nil || conflict["code"] != code || conflict["layer"] != "repository_instructions" ||
			conflict["repository_id"] != "repo-1" || conflict["winner"] != "platform_safety" {
			t.Fatalf("conflict for %s = %v (all: %v)", path, conflict, reports[0]["conflicts"])
		}
	}
	for _, raw := range reports[0]["layers"].([]any) {
		layer := raw.(map[string]any)
		for _, rawSource := range layer["sources"].([]any) {
			source := rawSource.(map[string]any)
			if _, conflicting := want[fmt.Sprint(source["path"])]; conflicting {
				t.Fatalf("layer %v claims %v as loaded", layer["layer"], source["path"])
			}
		}
	}
	if after, err := worktreeFingerprint(context.Background(), checkout); err != nil || after != fingerprint {
		t.Fatalf("checkout changed by composition: %v", err)
	}
}
