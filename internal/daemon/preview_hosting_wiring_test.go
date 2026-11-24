package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/repo"
)

func previewHostingWiringDaemon(t *testing.T) *Daemon {
	t.Helper()
	jr, _ := testJobRunner(t)
	d := jr.d
	d.runnerID = "runner-1"
	d.cfg.PreviewHosting = config.PreviewHostingConfig{Enabled: true, MaxConcurrent: 2, MemoryBudgetMB: 4096}
	d.hostMemory = func() uint64 { return 8 << 30 }
	d.previewHostGOOS = func() string { return "linux" }
	return d
}

func TestPreviewHostControllerWiringReturnsTheDockerController(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	controller, err := d.newPreviewHostController()
	if err != nil {
		t.Fatal(err)
	}
	if _, docker := controller.(*previewhost.Docker); !docker {
		t.Fatalf("production controller = %T, want *previewhost.Docker", controller)
	}
	if capability := controller.Capability(); capability.MaxConcurrent != 2 {
		t.Fatalf("capability=%+v", capability)
	}
	if _, listing := controller.(previewHostIDLister); !listing {
		t.Fatal("production controller does not list hosted preview ids for the orphan sweep")
	}
}

func TestPreviewHostControllerWiringRefusesNonLinuxHosts(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "freebsd"} {
		d := previewHostingWiringDaemon(t)
		d.previewHostGOOS = func() string { return goos }
		if _, err := d.newPreviewHostController(); err == nil || err.Error() != "preview hosting needs a Linux runner host" {
			t.Fatalf("%s: err=%v", goos, err)
		}
	}
}

func TestPreviewHostControllerWiringRequiresAKnownMemoryBudget(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	d.cfg.PreviewHosting.MemoryBudgetMB = 0
	d.hostMemory = func() uint64 { return 0 }
	if _, err := d.newPreviewHostController(); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("err=%v", err)
	}
	d.hostMemory = func() uint64 { return 8 << 30 }
	if _, err := d.newPreviewHostController(); err != nil {
		t.Fatal(err)
	}
	if budget := d.previewHostMemoryBudgetMB(); budget != 4096 {
		t.Fatalf("default budget=%d", budget)
	}
}

func TestPreviewHostControllerWiringRejectsAnInconsistentLease(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	d.cfg.PreviewHosting.LeaseMaxSeconds = 60
	if _, err := d.newPreviewHostController(); err == nil {
		t.Fatal("phase timeouts beyond the lease were accepted")
	}
}

func TestPreviewHostRepositoryResolverUsesConfiguredRepositories(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	d.cfg.Repositories = map[string]config.RepositoryConfig{
		"web": {URL: "https://github.com/org/web.git"},
		"api": {URL: "https://git.example.com/org/api.git"},
	}
	d.cfg.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{
		"main": {Provider: "github"},
	}
	resolver := previewHostRepositories{d: d}
	credential := &v1.ProviderCredential{
		Kind: v1.ProviderCredentialGitHubInstallationToken, Token: "token-value",
		ExpiresAt: time.Now().Add(time.Hour), RepositoryIDs: []string{"42"},
	}
	resolved, err := resolver.ResolveRepository(context.Background(), "web", credential)
	if err != nil || resolved.URL != "https://github.com/org/web.git" || resolved.Credential == nil ||
		resolved.Credential.Password != "token-value" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	resolved, err = resolver.ResolveRepository(context.Background(), "api", credential)
	if err != nil || resolved.Credential != nil {
		t.Fatalf("non-github origin received a credential: %+v err=%v", resolved, err)
	}
	expired := *credential
	expired.ExpiresAt = time.Now().Add(30 * time.Second)
	resolved, err = resolver.ResolveRepository(context.Background(), "web", &expired)
	if err != nil || resolved.Credential != nil {
		t.Fatalf("expired credential resolved=%+v err=%v", resolved, err)
	}
	if _, err := resolver.ResolveRepository(context.Background(), "missing", credential); err == nil {
		t.Fatal("unknown repository resolved")
	}
}

func TestPreviewHostRepositoryResolverLogsTheRedactedCredentialCause(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	var logs bytes.Buffer
	d.logger = slog.New(slog.NewTextHandler(&logs, nil))
	d.cfg.Repositories = map[string]config.RepositoryConfig{"web": {URL: "https://github.com/org/web.git"}}
	d.cfg.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{"main": {Provider: "github"}}
	token := "ghs-unusable-preview-token-value"
	credential := &v1.ProviderCredential{
		Kind: v1.ProviderCredentialGitHubInstallationToken, Token: token,
		ExpiresAt: time.Now().Add(30 * time.Second), RepositoryIDs: []string{"42"},
	}
	resolved, err := previewHostRepositories{d: d}.ResolveRepository(context.Background(), "web", credential)
	if err != nil || resolved.Credential != nil {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	output := logs.String()
	if !strings.Contains(output, repo.ErrRepositoryCredentialUnavailable.Error()) || !strings.Contains(output, "web") {
		t.Fatalf("credential cause not logged: %s", output)
	}
	if strings.Contains(output, token) {
		t.Fatalf("token leaked into the log: %s", output)
	}
}

func TestPreviewHostOwnershipAdapterScopesRequestsToThePreview(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	recorder := &recordingOwnership{log: &orderLog{}}
	adapter := previewHostOwnership{ownership: recorder, runnerID: d.runnerID, stateDir: d.cfg.StateDir}
	preview := filepath.Join(d.cfg.StateDir, "previews", "0123456789ab")
	workspace := filepath.Join(preview, "workspace")
	if err := adapter.HandOff(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reclaim(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if len(recorder.handOffs) != 1 || len(recorder.reclaims) != 1 {
		t.Fatalf("handOffs=%v reclaims=%v", recorder.handOffs, recorder.reclaims)
	}
	handOff, reclaim := recorder.handOffs[0], recorder.reclaims[0]
	if handOff.RunnerID != "runner-1" || handOff.AttemptID != "preview-0123456789ab" || handOff.Purpose != "preview" ||
		len(handOff.Workspace) != 1 || handOff.Workspace[0] != workspace || len(handOff.Private) != 0 {
		t.Fatalf("handOff=%+v", handOff)
	}
	if reclaim.AttemptID != "preview-0123456789ab" || len(reclaim.Private) != 1 || reclaim.Private[0] != preview ||
		len(reclaim.Workspace) != 0 {
		t.Fatalf("reclaim=%+v", reclaim)
	}
	for _, outside := range []string{filepath.Join(d.cfg.StateDir, "jobs", "job-1"), filepath.Join(d.cfg.StateDir, "previews", "../jobs")} {
		if err := adapter.Reclaim(context.Background(), outside); err == nil {
			t.Fatalf("adapter accepted %s", outside)
		}
	}
}

func TestPreviewHostControllerWiringWithoutOwnershipHelper(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	d.ownership = nil
	if _, err := d.newPreviewHostController(); err != nil {
		t.Fatal(err)
	}
	if ownership := d.previewHostWorkspaceOwnership(); ownership != nil {
		t.Fatal("nil ownership helper produced an adapter")
	}
	d.ownership = &recordingOwnership{log: &orderLog{}}
	if ownership := d.previewHostWorkspaceOwnership(); ownership == nil {
		t.Fatal("ownership helper was not adapted")
	}
}

func TestPinnedWorkspacesRefuseAnonymousFetch(t *testing.T) {
	d := previewHostingWiringDaemon(t)
	workspaces := pinnedWorkspaces{manager: d.previewHostRepositoryManager()}
	dest := filepath.Join(t.TempDir(), "workspace")
	err := workspaces.CreateDetachedWorkspaceWithCredential(context.Background(),
		"https://github.com/org/web.git", strings.Repeat("a", 40), dest, nil)
	if !errors.Is(err, repo.ErrPinnedCommitNeedsCredential) {
		t.Fatalf("err=%v", err)
	}
}
