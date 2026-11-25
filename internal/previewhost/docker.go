package previewhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/redact"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/validation"
)

const (
	workspaceMount   = "/workspace"
	appTmpfs         = "/tmp:rw,nosuid,nodev,size=256m"
	phaseLogLimit    = 64 << 10
	maxCheckDuration = 120_000
)

var roleCapabilities = map[string][]string{
	"database":  {"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"},
	"cache":     {"SETGID", "SETUID"},
	"mail_sink": nil,
	"other":     nil,
}

type WorkspaceCreator interface {
	CreateDetachedWorkspaceWithCredential(ctx context.Context, url, sha, dest string, credential *repo.GitHTTPSCredential) error
}

type ResolvedRepository struct {
	URL        string
	Credential *repo.GitHTTPSCredential
}

type RepositoryResolver interface {
	ResolveRepository(ctx context.Context, repositoryID string, credential *v1.ProviderCredential) (ResolvedRepository, error)
}

type WorkspaceOwnership interface {
	HandOff(ctx context.Context, dir string) error
	Reclaim(ctx context.Context, dir string) error
}

type Dependencies struct {
	Engine       Engine
	Workspaces   WorkspaceCreator
	Repositories RepositoryResolver
	Ownership    WorkspaceOwnership
}

type Docker struct {
	cfg            Config
	engine         Engine
	workspaces     WorkspaceCreator
	repositories   RepositoryResolver
	ownership      WorkspaceOwnership
	lookup         func(context.Context, string) ([]netip.Addr, error)
	random         func() (string, error)
	dial           func(context.Context, string, string) (net.Conn, error)
	selfExecutable func() (string, error)
	client         *http.Client

	versionMu      sync.Mutex
	versionChecked bool
}

var (
	_ Controller     = (*Docker)(nil)
	_ IngressLocator = (*Docker)(nil)
)

func New(cfg Config, deps Dependencies) (*Docker, error) {
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if deps.Engine == nil || deps.Workspaces == nil || deps.Repositories == nil {
		return nil, errors.New("preview host requires an engine, a workspace creator and a repository resolver")
	}
	return &Docker{
		cfg: normalized, engine: deps.Engine, workspaces: deps.Workspaces,
		repositories: deps.Repositories, ownership: deps.Ownership,
		lookup: defaultLookup, random: randomSecret, dial: (&net.Dialer{}).DialContext,
		selfExecutable: defaultSelfExecutable,
		client: &http.Client{
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (d *Docker) Capability() v1.PreviewHostingCapability {
	maxExtension := uint64(d.cfg.MaxExtension.Milliseconds())
	return v1.PreviewHostingCapability{
		ProtocolVersion: v1.PreviewHostingProtocolVersion,
		MaxConcurrent:   d.cfg.MaxConcurrent,
		Actions: []v1.PreviewLifecycleAction{
			"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe",
		},
		MaxExtensionMS: &maxExtension,
	}
}

func (d *Docker) previewDir(previewID string) string {
	return filepath.Join(d.cfg.StateDir, "previews", strings.TrimPrefix(ResourcePrefix(d.cfg.RunnerID, previewID), "our-preview-"))
}

func (d *Docker) appName(previewID string) string {
	return ResourcePrefix(d.cfg.RunnerID, previewID) + "-app"
}

type phaseFailure struct {
	phase  v1.PreviewDeployPhase
	log    string
	checks []v1.PreviewObservationCheck
	cause  error
}

func (d *Docker) Deploy(ctx context.Context, request DeployRequest) DeployResult {
	previewID := request.Operation.PreviewID
	if err := d.teardownDetached(ctx, previewID); err != nil {
		return DeployResult{Outcome: v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}}
	}
	checks, failure := d.deploy(ctx, request)
	if failure == nil {
		return DeployResult{Outcome: v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: checks}}
	}
	if err := d.teardownDetached(ctx, previewID); err != nil {
		return DeployResult{Outcome: v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}}
	}
	outcome := v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: failure.phase, Checks: failure.checks}
	if errors.Is(failure.cause, context.DeadlineExceeded) {
		outcome.Phase = v1.PreviewDeployPhaseLeaseExpired
		outcome.Checks = nil
	}
	outcome.LogTail = boundLogTail(redact.KnownSecrets(failure.log))
	return DeployResult{Outcome: outcome}
}

type deployment struct {
	request   DeployRequest
	prefix    string
	labels    map[string]string
	workspace string
	env       environment
}

func (d *Docker) deploy(ctx context.Context, request DeployRequest) ([]v1.PreviewObservationCheck, *phaseFailure) {
	previewID := request.Operation.PreviewID
	run := &deployment{
		request: request, prefix: ResourcePrefix(d.cfg.RunnerID, previewID),
		labels:    PreviewLabels(d.cfg.RunnerID, previewID),
		workspace: filepath.Join(d.previewDir(previewID), "workspace"),
	}
	env, err := resolveEnvironment(request.Content, request.Operation, d.random)
	if err == nil {
		err = checkReservedServiceNames(request.Content)
	}
	if err != nil {
		return nil, &phaseFailure{phase: v1.PreviewDeployPhaseValidate, log: "error: " + err.Error() + "\n"}
	}
	run.env = env
	steps := []struct {
		phase v1.PreviewDeployPhase
		run   func(context.Context, *deployment, *tailBuffer) error
	}{
		{v1.PreviewDeployPhaseCheckout, d.checkout},
		{v1.PreviewDeployPhasePull, d.pull},
		{v1.PreviewDeployPhaseServices, d.services},
		{v1.PreviewDeployPhaseInstall, d.commands(v1.PreviewDeployPhaseInstall, request.Content.App.Install)},
		{v1.PreviewDeployPhaseBuild, d.commands(v1.PreviewDeployPhaseBuild, request.Content.App.Build)},
		{v1.PreviewDeployPhaseMigrate, d.commands(v1.PreviewDeployPhaseMigrate, request.Content.App.Migrate)},
		{v1.PreviewDeployPhaseSeed, d.commands(v1.PreviewDeployPhaseSeed, request.Content.App.Seed)},
		{v1.PreviewDeployPhaseStart, d.start},
	}
	for _, step := range steps {
		log := &tailBuffer{limit: phaseLogLimit}
		phaseCtx, cancel := context.WithTimeout(ctx, d.cfg.PhaseTimeouts[step.phase])
		err := step.run(phaseCtx, run, log)
		cancel()
		if err != nil {
			cause := ctx.Err()
			_, _ = log.Write([]byte("error: " + redact.KnownSecretsError(err).Error() + "\n"))
			phase := step.phase
			if errors.As(err, new(validationError)) {
				phase = v1.PreviewDeployPhaseValidate
			}
			return nil, &phaseFailure{phase: phase, log: log.String(), cause: cause}
		}
	}
	phaseCtx, cancel := context.WithTimeout(ctx, d.cfg.PhaseTimeouts[v1.PreviewDeployPhaseHealth])
	defer cancel()
	checks, passed := d.health(phaseCtx, run)
	if passed {
		return checks, nil
	}
	cause := ctx.Err()
	log := &tailBuffer{limit: phaseLogLimit}
	logsCtx, logsCancel := context.WithTimeout(context.WithoutCancel(ctx), logGrace)
	defer logsCancel()
	logs, _ := d.engine.Logs(logsCtx, run.prefix+"-app")
	_, _ = log.Write([]byte(logs))
	return nil, &phaseFailure{phase: v1.PreviewDeployPhaseHealth, log: log.String(), checks: checks, cause: cause}
}

func (d *Docker) checkout(ctx context.Context, run *deployment, _ *tailBuffer) error {
	if err := d.checkDockerVersion(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(run.workspace), 0o700); err != nil {
		return err
	}
	commits := run.request.Operation.CommitSet
	if len(commits) == 0 {
		return errors.New("commit set is empty")
	}
	if len(commits) > 1 {
		if err := os.Mkdir(run.workspace, 0o700); err != nil {
			return err
		}
	}
	for _, commit := range commits {
		resolved, err := d.repositories.ResolveRepository(ctx, commit.RepositoryID, run.request.Credential)
		if err != nil {
			return fmt.Errorf("resolve repository %s: %w", commit.RepositoryID, err)
		}
		dest := run.workspace
		if len(commits) > 1 {
			dest = filepath.Join(run.workspace, commit.RepositoryID)
		}
		if err := d.workspaces.CreateDetachedWorkspaceWithCredential(ctx, resolved.URL, commit.CommitSHA, dest, resolved.Credential); err != nil {
			return fmt.Errorf("check out repository %s: %w", commit.RepositoryID, err)
		}
	}
	if d.ownership != nil {
		return d.ownership.HandOff(ctx, run.workspace)
	}
	return nil
}

func (d *Docker) pull(ctx context.Context, run *deployment, log *tailBuffer) error {
	var images []string
	for _, service := range run.request.Content.Services {
		images = append(images, service.Image)
	}
	images = append(images, run.request.Content.App.Image)
	seen := map[string]bool{}
	for _, image := range images {
		if seen[image] {
			continue
		}
		seen[image] = true
		present, err := d.engine.ImagePresent(ctx, image)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if err := checkRegistryHost(ctx, d.lookup, registryHost(image)); err != nil {
			return fmt.Errorf("image %s: %w", image, err)
		}
		output, err := d.engine.Pull(ctx, image)
		_, _ = log.Write([]byte(output))
		if err != nil {
			return fmt.Errorf("pull %s: %w", image, err)
		}
	}
	return nil
}

func (d *Docker) baseSpec(run *deployment, image string) ContainerSpec {
	limits := run.request.Content.Limits
	return ContainerSpec{
		Image: image, Network: run.prefix, Labels: run.labels,
		MemoryMB: limits.MemoryMB, CPUs: limits.CPUs, PidsLimit: defaultPidsLimit,
	}
}

func (d *Docker) services(ctx context.Context, run *deployment, log *tailBuffer) error {
	network := NetworkSpec{
		Name: run.prefix, Labels: run.labels, Internal: true,
		Options: map[string]string{
			"com.docker.network.bridge.inhibit_ipv4": "true",
			"com.docker.network.enable_ipv6":         "false",
		},
	}
	if err := d.engine.CreateNetwork(ctx, network); err != nil {
		return err
	}
	if err := d.startGateway(ctx, run); err != nil {
		return err
	}
	for _, service := range run.request.Content.Services {
		spec := d.baseSpec(run, service.Image)
		spec.Name = run.prefix + "-svc-" + service.Name
		spec.NetworkAlias = service.Name
		spec.Env = run.env.services[service.Name]
		spec.CapAdd = slices.Clone(roleCapabilities[service.Role])
		if err := d.engine.RunDetached(ctx, spec); err != nil {
			return fmt.Errorf("start service %s: %w", service.Name, err)
		}
	}
	for _, service := range run.request.Content.Services {
		if service.Health == nil {
			continue
		}
		if err := d.waitForService(ctx, run.prefix+"-svc-"+service.Name, *service.Health, log); err != nil {
			return fmt.Errorf("service %s is not healthy: %w", service.Name, err)
		}
	}
	return nil
}

func (d *Docker) waitForService(ctx context.Context, container string, health v1.PreviewBlueprintServiceHealthV1, log *tailBuffer) error {
	interval := time.Duration(health.IntervalSeconds) * time.Second
	for {
		output, err := d.engine.Exec(ctx, container, health.Command)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			_, _ = log.Write([]byte(output))
			return ctx.Err()
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_, _ = log.Write([]byte(output))
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *Docker) appSpec(run *deployment) ContainerSpec {
	app := run.request.Content.App
	spec := d.baseSpec(run, app.Image)
	env := make(map[string]string, len(run.env.app)+1)
	for key, value := range run.env.app {
		env[key] = value
	}
	if _, set := env["HOME"]; !set {
		env["HOME"] = "/tmp"
	}
	applyProxyEnvironment(env, run.request.Content)
	spec.Env = env
	spec.User = strconv.Itoa(d.cfg.JobUID) + ":" + strconv.Itoa(d.cfg.JobGID)
	spec.ReadOnly = true
	spec.Tmpfs = []string{appTmpfs}
	spec.Mounts = []Mount{{Source: run.workspace, Target: workspaceMount}}
	spec.WorkDir = path.Join(workspaceMount, app.WorkingDirectory)
	spec.Entrypoint = "/bin/sh"
	return spec
}

func (d *Docker) commands(phase v1.PreviewDeployPhase, commands []string) func(context.Context, *deployment, *tailBuffer) error {
	return func(ctx context.Context, run *deployment, log *tailBuffer) error {
		for index, command := range commands {
			spec := d.appSpec(run)
			spec.Name = run.prefix + "-run-" + string(phase) + "-" + strconv.Itoa(index)
			spec.Command = []string{"-c", command}
			output, err := d.engine.RunOnce(ctx, spec)
			_, _ = log.Write([]byte(output))
			if err != nil {
				return fmt.Errorf("%s command %d: %w", phase, index+1, err)
			}
		}
		return nil
	}
}

func (d *Docker) start(ctx context.Context, run *deployment, _ *tailBuffer) error {
	spec := d.appSpec(run)
	spec.Name = run.prefix + "-app"
	spec.NetworkAlias = appAlias
	spec.Command = []string{"-c", run.request.Content.App.Start}
	return d.engine.RunDetached(ctx, spec)
}

func (d *Docker) health(ctx context.Context, run *deployment) ([]v1.PreviewObservationCheck, bool) {
	checks := slices.Clone(run.request.Content.App.HealthChecks)
	slices.SortFunc(checks, func(a, b v1.PreviewBlueprintHealthCheckV1) int { return strings.Compare(a.ID, b.ID) })
	var last []v1.PreviewObservationCheck
	for {
		port, err := d.engine.HostPort(ctx, gatewayName(run.prefix), gatewayIngressPort)
		if err == nil {
			results, complete := d.probe(ctx, port, checks)
			if complete {
				last = results
				if allPassed(results) {
					return results, true
				}
			}
		}
		timer := time.NewTimer(d.cfg.HealthInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, false
		case <-timer.C:
		}
	}
}

func allPassed(results []v1.PreviewObservationCheck) bool {
	for _, result := range results {
		if result.Status != v1.PreviewObservationCheckPassed {
			return false
		}
	}
	return true
}

func (d *Docker) probe(ctx context.Context, port int, checks []v1.PreviewBlueprintHealthCheckV1) ([]v1.PreviewObservationCheck, bool) {
	results := make([]v1.PreviewObservationCheck, 0, len(checks))
	for _, check := range checks {
		if !strings.HasPrefix(check.Path, "/") || strings.HasPrefix(check.Path, "//") {
			return nil, false
		}
		target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: check.Path}
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		started := time.Now()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
		if err != nil || request.URL.Hostname() != "127.0.0.1" {
			cancel()
			return nil, false
		}
		response, err := d.client.Do(request)
		elapsed := time.Since(started).Milliseconds()
		cancel()
		if err != nil {
			return nil, false
		}
		_ = response.Body.Close()
		if response.StatusCode < 100 || response.StatusCode > 599 {
			return nil, false
		}
		status := v1.PreviewObservationCheckFailed
		if response.StatusCode == check.ExpectedStatus {
			status = v1.PreviewObservationCheckPassed
		}
		results = append(results, v1.PreviewObservationCheck{
			CheckID: check.ID, Status: status, HTTPStatus: response.StatusCode,
			DurationMS: uint64(min(max(elapsed, 0), maxCheckDuration)),
		})
	}
	return results, true
}

func (d *Docker) ApplyLifecycle(ctx context.Context, request LifecycleRequest) LifecycleResult {
	operation := request.Operation
	switch operation.Action {
	case v1.PreviewLifecycleExtend:
		return LifecycleResult{Outcome: d.extend(ctx, operation)}
	case v1.PreviewLifecycleDestroy:
		return LifecycleResult{Outcome: d.destroy(ctx, operation.PreviewID)}
	}
	return LifecycleResult{Outcome: v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecyclePolicyDenied}}
}

func (d *Docker) extend(ctx context.Context, operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	if operation.DesiredExpiresAt == nil {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecyclePolicyDenied}
	}
	extension := operation.DesiredExpiresAt.Sub(operation.CurrentEffectiveExpiresAt)
	if extension <= 0 || extension > d.cfg.MaxExtension {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecyclePolicyDenied}
	}
	objects, err := d.engine.List(ctx, PreviewLabels(d.cfg.RunnerID, operation.PreviewID))
	if err != nil {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleControllerUnavailable}
	}
	if objects.Empty() {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}
	}
	running, err := d.engine.Running(ctx, d.appName(operation.PreviewID))
	if err != nil {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleControllerUnavailable}
	}
	if !running {
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}
	}
	blueprint := operation.Blueprint
	expiresAt := *operation.DesiredExpiresAt
	return v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded,
		ResourceState: &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceActive, Blueprint: &blueprint,
			CommitSet: slices.Clone(operation.CommitSet), ExpiresAt: &expiresAt,
		},
	}
}

func (d *Docker) destroy(ctx context.Context, previewID string) v1.PreviewLifecycleResultOutcome {
	uncertain := v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleReceiptUnavailable}
	objects, err := d.engine.List(ctx, PreviewLabels(d.cfg.RunnerID, previewID))
	if err != nil {
		return uncertain
	}
	disposition := v1.PreviewLifecycleDestroyed
	if objects.Empty() {
		disposition = v1.PreviewLifecycleAlreadyAbsent
	}
	if err := d.teardown(ctx, previewID); err != nil {
		return uncertain
	}
	return v1.PreviewLifecycleResultOutcome{
		Kind:          v1.PreviewLifecycleSucceeded,
		ResourceState: &v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: disposition},
	}
}

func (d *Docker) Recover(ctx context.Context, host journal.PreviewHost) error {
	teardownCtx, cancel := context.WithTimeout(ctx, d.cfg.TeardownTimeout)
	defer cancel()
	return d.teardown(teardownCtx, host.PreviewID)
}

func (d *Docker) IngressAddress(ctx context.Context, previewID string) (string, error) {
	port, err := d.engine.HostPort(ctx, gatewayName(ResourcePrefix(d.cfg.RunnerID, previewID)), gatewayIngressPort)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

func (d *Docker) HostedPreviewIDs(ctx context.Context) ([]string, error) {
	raw, err := d.engine.HostPreviewIDs(ctx, d.cfg.RunnerID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(raw))
	var invalid error
	for _, id := range raw {
		if err := validation.ID("preview_id", id); err != nil {
			invalid = errors.New("docker reported a preview resource with an invalid preview id label")
			continue
		}
		ids = append(ids, id)
	}
	return ids, invalid
}

func (d *Docker) teardownDetached(ctx context.Context, previewID string) error {
	teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.TeardownTimeout)
	defer cancel()
	return d.teardown(teardownCtx, previewID)
}

func (d *Docker) teardown(ctx context.Context, previewID string) error {
	labels := PreviewLabels(d.cfg.RunnerID, previewID)
	objects, err := d.engine.List(ctx, labels)
	if err != nil {
		return err
	}
	if !objects.Empty() {
		if err := d.engine.Remove(ctx, objects); err != nil {
			return err
		}
		remaining, err := d.engine.List(ctx, labels)
		if err != nil {
			return err
		}
		if !remaining.Empty() {
			return errors.New("preview resources survived removal")
		}
	}
	dir := d.previewDir(previewID)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if d.ownership != nil {
		if err := d.ownership.Reclaim(ctx, dir); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

func boundLogTail(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > v1.PreviewDeployLogTailMaxBytes {
		value = value[len(value)-v1.PreviewDeployLogTailMaxBytes:]
		for len(value) > 0 && !utf8.RuneStart(value[0]) {
			value = value[1:]
		}
	}
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > v1.PreviewDeployLogTailMaxLines {
		value = strings.Join(lines[len(lines)-v1.PreviewDeployLogTailMaxLines:], "")
	}
	return value
}
