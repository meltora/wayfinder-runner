package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/repo"
)

const previewEgressUnavailable = "preview egress enforcement is not available yet"

var previewHostDirPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

type egressGuardedPreviewHost struct {
	*previewhost.Docker
}

func (egressGuardedPreviewHost) Deploy(context.Context, previewhost.DeployRequest) previewhost.DeployResult {
	return previewhost.DeployResult{Outcome: v1.PreviewDeployOutcome{
		Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseValidate, LogTail: previewEgressUnavailable,
	}}
}

func previewHostSeconds(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

func (d *Daemon) newPreviewHostController() (previewhost.Controller, error) {
	hosting := d.cfg.PreviewHosting
	if d.previewHostMemoryBudgetMB() <= 0 {
		return nil, errors.New("host memory is unknown; set preview_hosting.memory_budget_mb")
	}
	phases := make(map[v1.PreviewDeployPhase]time.Duration, len(hosting.PhaseTimeoutSeconds))
	for phase, seconds := range hosting.PhaseTimeoutSeconds {
		phases[v1.PreviewDeployPhase(phase)] = previewHostSeconds(seconds)
	}
	docker, err := previewhost.New(previewhost.Config{
		RunnerID:        d.runnerID,
		StateDir:        d.cfg.StateDir,
		Lease:           previewHostSeconds(hosting.LeaseMaxSeconds),
		PhaseTimeouts:   phases,
		TeardownTimeout: previewHostSeconds(hosting.TeardownTimeoutSeconds),
		MaxConcurrent:   d.previewHostMaxConcurrent(),
		MaxExtension:    previewHostSeconds(hosting.MaxExtensionSeconds),
		JobUID:          d.userMapping.ContainerUID,
		JobGID:          d.userMapping.ContainerGID,
	}, previewhost.Dependencies{
		Engine:       previewhost.DockerCLI{TempDir: filepath.Join(d.cfg.StateDir, "preview-tmp")},
		Workspaces:   pinnedWorkspaces{manager: d.previewHostRepositoryManager()},
		Repositories: previewHostRepositories{d: d},
		Ownership:    d.previewHostWorkspaceOwnership(),
	})
	if err != nil {
		return nil, err
	}
	return egressGuardedPreviewHost{Docker: docker}, nil
}

func (d *Daemon) previewHostRepositoryManager() *repo.Manager {
	rm := repo.NewManager(filepath.Join(d.cfg.StateDir, "repos"))
	rm.AllowLocal = d.cfg.AllowUnsafeDirect
	if d.configureRepositoryManager != nil {
		d.configureRepositoryManager(rm)
	}
	return rm
}

func (d *Daemon) previewHostWorkspaceOwnership() previewhost.WorkspaceOwnership {
	if d.ownership == nil {
		return nil
	}
	return previewHostOwnership{ownership: d.ownership, runnerID: d.runnerID, stateDir: d.cfg.StateDir}
}

type pinnedWorkspaces struct {
	manager *repo.Manager
}

func (workspaces pinnedWorkspaces) CreateDetachedWorkspaceWithCredential(
	ctx context.Context,
	url, sha, dest string,
	credential *repo.GitHTTPSCredential,
) error {
	return workspaces.manager.CreatePinnedWorkspace(ctx, url, sha, dest, credential)
}

type previewHostRepositories struct {
	d *Daemon
}

func (repositories previewHostRepositories) ResolveRepository(
	_ context.Context,
	repositoryID string,
	credential *v1.ProviderCredential,
) (previewhost.ResolvedRepository, error) {
	hot := repositories.d.hotConfiguration()
	repository, ok := hot.repositories[repositoryID]
	if !ok || repository.URL == "" {
		return previewhost.ResolvedRepository{}, fmt.Errorf("repository %q is not configured on this runner", repositoryID)
	}
	gitCredential, _, err := repositoryCredential(credential, repository.URL, hot.sourcePublicationInstallations, time.Now())
	if err != nil {
		gitCredential = nil
	}
	return previewhost.ResolvedRepository{URL: repository.URL, Credential: gitCredential}, nil
}

type previewHostOwnership struct {
	ownership isolation.Ownership
	runnerID  string
	stateDir  string
}

func (ownership previewHostOwnership) request(dir string) (isolation.OwnershipRequest, error) {
	rel, err := filepath.Rel(filepath.Join(ownership.stateDir, "previews"), dir)
	if err != nil || !filepath.IsLocal(rel) {
		return isolation.OwnershipRequest{}, fmt.Errorf("ownership path %s is not inside a preview directory", dir)
	}
	id, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	if !previewHostDirPattern.MatchString(id) {
		return isolation.OwnershipRequest{}, fmt.Errorf("ownership path %s is not inside a preview directory", dir)
	}
	return isolation.OwnershipRequest{RunnerID: ownership.runnerID, AttemptID: "preview-" + id, Purpose: "preview"}, nil
}

func (ownership previewHostOwnership) HandOff(ctx context.Context, dir string) error {
	request, err := ownership.request(dir)
	if err != nil {
		return err
	}
	request.Workspace = []string{dir}
	return ownership.ownership.HandOff(ctx, request)
}

func (ownership previewHostOwnership) Reclaim(ctx context.Context, dir string) error {
	request, err := ownership.request(dir)
	if err != nil {
		return err
	}
	request.Private = []string{dir}
	return ownership.ownership.Reclaim(ctx, request)
}
