package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

const integrationProviderToken = "ghs_integration000000000000000000000000000"

func integrationProviderCredential(expiresAt time.Time) v1.ProviderCredential {
	return v1.ProviderCredential{
		Kind:          v1.ProviderCredentialGitHubInstallationToken,
		Token:         integrationProviderToken,
		ExpiresAt:     expiresAt,
		RepositoryIDs: []string{"424242"},
	}
}

func startCredentialTestRunner(t *testing.T, configure func(*config.Config)) (*mockcp.Server, string, context.Context) {
	t.Helper()
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	if configure != nil {
		configure(&cfg)
	}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = d.Run(ctx) }()
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	return cp, stateDir, ctx
}

func treeContains(t *testing.T, root, needle string) []string {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// #nosec G304 -- test reads files it created under t.TempDir.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(needle)) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func jobWorkspaces(t *testing.T, stateDir string) []string {
	t.Helper()
	workspaces, err := filepath.Glob(filepath.Join(stateDir, "jobs", "job-*"))
	if err != nil {
		t.Fatal(err)
	}
	return workspaces
}

func TestAgentProcessNeverReceivesProviderCredential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	agentDir := t.TempDir()
	envDump := filepath.Join(agentDir, "agent-env")
	started := filepath.Join(agentDir, "agent-started")
	release := filepath.Join(agentDir, "agent-release")
	origin := seedBareOrigin(t)
	fakeAgent := writeFakeAgent(t, `env > "`+envDump+`"
: > "`+started+`"
i=0
while [ ! -e "`+release+`" ] && [ "$i" -lt 300 ]; do
  sleep 0.1
  i=$((i + 1))
done
echo '{"type":"result","session_id":"sess-credential","status":"success"}'
`)
	cp, stateDir, ctx := startCredentialTestRunner(t, func(cfg *config.Config) {
		cfg.Provider = "claude"
		cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
		cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	})

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "build-credential", AttemptID: "att-credential", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-1", origin)}, ApprovedPlanRevision: 1,
		PushBranch: true, PRTitle: "PROJ-9 add feature",
		Ticket: v1.TicketContext{Key: "PROJ-9", Summary: "Add Feature()"},
	})
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "start-job-with-credential",
		SentAt: time.Now(), Payload: v1.CommandPayload{Command: v1.CmdStartJob, Job: &job},
	}
	if err := cp.SendCommandWithProviderCredential(ctx, envelope, integrationProviderCredential(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent started", 60*time.Second, func() bool {
		_, err := os.Stat(started)
		return err == nil
	})
	if workspaces := jobWorkspaces(t, stateDir); len(workspaces) != 1 {
		t.Fatalf("paused job workspaces = %v", workspaces)
	}
	if matches := treeContains(t, stateDir, integrationProviderToken); len(matches) != 0 {
		t.Fatalf("provider token reached runner state while the agent ran: %v", matches)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job finished", 60*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})
	deadline := time.Now().Add(30 * time.Second)
	for len(jobWorkspaces(t, stateDir)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("job workspace was not removed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// #nosec G304 -- test reads the dump its fake agent wrote under t.TempDir.
	dump, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("agent environment dump: %v", err)
	}
	if !strings.Contains(string(dump), "PLATFORM_MCP_SOCKET=") {
		t.Fatalf("dump is not the agent environment:\n%s", dump)
	}
	for _, line := range strings.Split(string(dump), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if strings.Contains(line, integrationProviderToken) || value == integrationProviderToken ||
			key == "SHORTSTACK_GIT_PASSWORD" || key == "SHORTSTACK_GIT_USERNAME" || key == "GIT_ASKPASS" {
			t.Fatalf("agent environment carries provider credential material: %q", key)
		}
	}
	encodedEvents, err := json.Marshal(cp.Events())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedEvents), integrationProviderToken) {
		t.Fatal("events carry the provider token")
	}
	if matches := treeContains(t, stateDir, integrationProviderToken); len(matches) != 0 {
		t.Fatalf("provider token reached runner state: %v", matches)
	}
}

func TestTerminalCommandRedeliveryWithExpiredCredentialReplaysStoredAck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, _, ctx := startCredentialTestRunner(t, nil)
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "resume-with-credential",
		SentAt: time.Now(), Payload: v1.CommandPayload{Command: v1.CmdResumeRunner},
	}
	if err := cp.SendCommandWithProviderCredential(ctx, envelope, integrationProviderCredential(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "original acknowledgement", 10*time.Second, func() bool { return len(cp.CommandAcks()) == 1 })

	envelope.SentAt = time.Now()
	if err := cp.SendCommandWithProviderCredential(ctx, envelope, integrationProviderCredential(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replayed acknowledgement", 10*time.Second, func() bool { return len(cp.CommandAcks()) == 2 })
	for _, acknowledgement := range cp.CommandAcks() {
		payload := acknowledgement.Payload.(map[string]any)
		if acknowledgement.InReplyTo != envelope.ID || payload["status"] != "accepted" {
			t.Fatalf("acknowledgement = %+v", acknowledgement)
		}
	}

	fresh := envelope
	fresh.ID = "resume-with-expired-credential"
	fresh.SentAt = time.Now()
	if err := cp.SendCommandWithProviderCredential(ctx, fresh, integrationProviderCredential(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rejection acknowledgement", 10*time.Second, func() bool { return len(cp.CommandAcks()) == 3 })
	rejected := cp.CommandAcks()[2]
	payload := rejected.Payload.(map[string]any)
	if rejected.InReplyTo != fresh.ID || payload["status"] != "rejected" || payload["reason_code"] != "invalid_command" {
		t.Fatalf("fresh expired credential acknowledgement = %+v", rejected)
	}
	encodedAcks, err := json.Marshal(cp.CommandAcks())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAcks), integrationProviderToken) {
		t.Fatal("acknowledgements carry the provider token")
	}
}
