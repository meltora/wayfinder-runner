package daemon

import (
	"bytes"
	"context"
	"errors"
	"github.com/example/app-runner/internal/agentpack/trustset"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/remoteconfig"
)

type fakeConfigurationReloader struct {
	mu        sync.Mutex
	reloads   []remoteconfig.Reload
	errs      []error
	committed []remoteconfig.Reload
	refused   []remoteconfig.Reload
	commitErr error
}

func (f *fakeConfigurationReloader) Check(context.Context) (remoteconfig.Reload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reload, err := f.reloads[0], f.errs[0]
	if len(f.reloads) > 1 {
		f.reloads, f.errs = f.reloads[1:], f.errs[1:]
	}
	return reload, err
}

func (f *fakeConfigurationReloader) Commit(reload remoteconfig.Reload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, reload)
	return f.commitErr
}

func (f *fakeConfigurationReloader) Refuse(reload remoteconfig.Reload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused = append(f.refused, reload)
}

func (f *fakeConfigurationReloader) refusals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	digests := make([]string, 0, len(f.refused))
	for _, reload := range f.refused {
		digests = append(digests, reload.Delivery.Digest)
	}
	return digests
}

func (f *fakeConfigurationReloader) commits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.committed)
}

func reloadTestConfig(stateDir string) config.Config {
	cfg := config.Normalize(config.Config{
		StateDir: stateDir, ControlPlaneURL: "wss://gateway.example.test/v1/runners/stream",
		ControlPlaneHTTPURL: "https://gateway.example.test", Isolation: "docker", Provider: "claude",
		JobImage: "ghcr.io/example/job@sha256:" + strings.Repeat("a", 64),
		Labels:   map[string]string{"pool": "eu-linux"},
		Repositories: map[string]config.RepositoryConfig{
			"repo-1": {URL: "https://github.com/acme/api.git"},
		},
		AgentPackPublicKeys: map[string]string{"key-1": "c3BraS0x"},
	})
	cfg.RunnerConfigurationDigest = strings.Repeat("d", 64)
	return cfg
}

func reloadTestDaemon(t *testing.T) (*Daemon, *capturedEvents, *bytes.Buffer) {
	t.Helper()
	stateDir := t.TempDir()
	events := &capturedEvents{}
	logs := &bytes.Buffer{}
	d := &Daemon{
		cfg:       reloadTestConfig(stateDir),
		store:     identity.NewStore(stateDir),
		runnerID:  "runner-1",
		logger:    slog.New(slog.NewTextHandler(&lockedWriter{buffer: logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		eventSink: events.sink,
	}
	if _, err := d.store.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	d.controlReady.Store(true)
	return d, events, logs
}

type lockedWriter struct {
	mu     sync.Mutex
	buffer *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}

func hotReload(base config.Config, digest string, change func(*config.Config)) remoteconfig.Reload {
	next := base
	next.Labels = map[string]string{"pool": "us-linux"}
	next.Repositories = map[string]config.RepositoryConfig{
		"repo-1": {URL: "https://github.com/acme/api.git"},
		"repo-2": {URL: "https://github.com/acme/web.git"},
	}
	next.AgentPackPublicKeys = map[string]string{"key-2": "c3BraS0y"}
	next.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github"}}
	next.RunnerConfigurationDigest = ""
	if change != nil {
		change(&next)
	}
	return remoteconfig.Reload{
		Outcome:  remoteconfig.ReloadApply,
		Delivery: remoteconfig.Delivery{Digest: digest},
		Merged:   remoteconfig.Merged{Config: next},
		Raw:      []byte(`{}`),
	}
}

func publishedManifests(t *testing.T, events *capturedEvents) []v1.CapabilityManifest {
	t.Helper()
	var manifests []v1.CapabilityManifest
	for _, event := range events.byKind("capability_manifest") {
		manifest, ok := event["manifest"].(v1.CapabilityManifest)
		if !ok {
			t.Fatalf("manifest = %#v", event["manifest"])
		}
		manifests = append(manifests, manifest)
	}
	return manifests
}

func TestReloadAppliesHotKeysAndPublishesTheNewDigest(t *testing.T) {
	d, events, _ := reloadTestDaemon(t)
	newDigest := strings.Repeat("e", 64)
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, newDigest, nil)}, errs: []error{nil}}
	d.configurationReloader = reloader
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload reported a failure")
	}
	hot := d.hotConfiguration()
	if hot.digest != newDigest || hot.labels["pool"] != "us-linux" || len(hot.repositories) != 2 ||
		!reflect.DeepEqual(hot.agentPackPublicKeys, map[string]string{"key-2": "c3BraS0y"}) || len(hot.sourcePublicationInstallations) != 1 {
		t.Fatalf("hot = %+v", hot)
	}
	if d.sourcePublicationServices().publisher == nil {
		t.Fatal("source publication services were not rebuilt for the new installation")
	}
	if reloader.commits() != 1 {
		t.Fatalf("commits = %d", reloader.commits())
	}
	manifests := publishedManifests(t, events)
	if len(manifests) != 1 {
		t.Fatalf("manifests = %d", len(manifests))
	}
	manifest := manifests[0]
	if manifest.RunnerConfigurationDigest != newDigest || manifest.Labels["pool"] != "us-linux" ||
		!reflect.DeepEqual(manifest.TrustedAgentPackKeyIDs, []string{"key-2"}) ||
		!reflect.DeepEqual(manifest.AgentConfigScanRepositoryIDs, []string{"repo-1", "repo-2"}) ||
		!reflect.DeepEqual(manifest.SourcePublicationProviders, []string{"github"}) {
		t.Fatalf("manifest = %+v", manifest)
	}
	if d.cfg.Provider != "claude" || d.cfg.Isolation != "docker" {
		t.Fatalf("restart-required settings changed: %+v", d.cfg)
	}
}

func TestReloadWhileDisconnectedAppliesWithoutPublishing(t *testing.T) {
	d, events, _ := reloadTestDaemon(t)
	d.controlReady.Store(false)
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), nil)}, errs: []error{nil}}
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload reported a failure")
	}
	if len(publishedManifests(t, events)) != 0 || d.hotConfiguration().digest != strings.Repeat("e", 64) {
		t.Fatal("a disconnected daemon published or did not apply")
	}
}

func TestReloadRefusesARestartRequiredDocumentAndWarnsOncePerDigest(t *testing.T) {
	d, events, logs := reloadTestDaemon(t)
	before := d.hotConfiguration()
	restart := remoteconfig.Reload{Outcome: remoteconfig.ReloadRestartRequired, Delivery: remoteconfig.Delivery{Digest: strings.Repeat("f", 64)}, RestartKeys: []string{"provider"}}
	other := remoteconfig.Reload{Outcome: remoteconfig.ReloadRestartRequired, Delivery: remoteconfig.Delivery{Digest: strings.Repeat("c", 64)}, RestartKeys: []string{"job_image"}}
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{restart, restart, other}, errs: []error{nil, nil, nil}}
	d.configurationReloader = reloader
	for range 3 {
		if !d.reloadConfiguration(context.Background()) {
			t.Fatal("a restart-required document counted as a fetch failure")
		}
	}
	if !reflect.DeepEqual(d.hotConfiguration(), before) || reloader.commits() != 0 || len(publishedManifests(t, events)) != 0 {
		t.Fatal("a restart-required document was applied")
	}
	if count := strings.Count(logs.String(), "runner configuration needs a restart"); count != 2 {
		t.Fatalf("restart warnings = %d\n%s", count, logs.String())
	}
	if !strings.Contains(logs.String(), "provider") || !strings.Contains(logs.String(), "job_image") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestReloadRefusesAMergedDocumentThatChangesRestartRequiredSettings(t *testing.T) {
	changes := map[string]func(*config.Config){
		"provider":    func(c *config.Config) { c.Provider = "codex" },
		"isolation":   func(c *config.Config) { c.Isolation = "direct"; c.AllowUnsafeDirect = true; c.JobImage = "" },
		"job_image":   func(c *config.Config) { c.JobImage = "ghcr.io/example/job@sha256:" + strings.Repeat("b", 64) },
		"job_network": func(c *config.Config) { c.JobNetwork = "restricted" },
		"job_memory":  func(c *config.Config) { c.JobMemoryMB = 1 },
		"job_cpus":    func(c *config.Config) { c.JobCPUs = 9 },
		"job_pids":    func(c *config.Config) { c.JobPIDs = 9 },
		"gateway":     func(c *config.Config) { c.ControlPlaneURL = "wss://other.example.test/v1/runners/stream" },
		"state_dir":   func(c *config.Config) { c.StateDir = "/elsewhere" },
		"binaries":    func(c *config.Config) { c.ProviderBinaries = map[string]string{"claude": "/tmp/claude"} },
		"environment": func(c *config.Config) {
			c.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/tmp"}}
		},
	}
	for name, change := range changes {
		d, events, _ := reloadTestDaemon(t)
		before := d.hotConfiguration()
		reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), change)}, errs: []error{nil}}
		d.configurationReloader = reloader
		if d.reloadConfiguration(context.Background()) {
			t.Fatalf("%s: reload reported success", name)
		}
		if !reflect.DeepEqual(d.hotConfiguration(), before) || reloader.commits() != 0 || len(publishedManifests(t, events)) != 0 {
			t.Fatalf("%s: applied", name)
		}
	}
}

func TestReloadFailuresKeepTheRunningConfigurationAndLogOncePerStreak(t *testing.T) {
	d, events, logs := reloadTestDaemon(t)
	before := d.hotConfiguration()
	failures := []error{
		&remoteconfig.StatusError{Status: 401, Code: "unauthorized"},
		&remoteconfig.NetworkError{Err: errors.New("connection refused")},
		&remoteconfig.StatusError{Status: 409, Code: "direct_isolation_unavailable"},
	}
	reloader := &fakeConfigurationReloader{
		reloads: []remoteconfig.Reload{{}, {}, {}, {Outcome: remoteconfig.ReloadUnchanged}},
		errs:    append(append([]error(nil), failures...), nil),
	}
	d.configurationReloader = reloader
	for range failures {
		if d.reloadConfiguration(context.Background()) {
			t.Fatal("a failure reported success")
		}
	}
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("recovery reported a failure")
	}
	if !reflect.DeepEqual(d.hotConfiguration(), before) || reloader.commits() != 0 || len(publishedManifests(t, events)) != 0 {
		t.Fatal("a failed fetch changed the running configuration")
	}
	output := logs.String()
	if count := strings.Count(output, "level=WARN msg=\"runner configuration refresh failed"); count != 1 {
		t.Fatalf("warnings = %d\n%s", count, output)
	}
	if !strings.Contains(output, "did not accept this runner's identity") {
		t.Fatalf("the first failure was not explained: %s", output)
	}
	if strings.Count(output, "runner configuration refresh recovered") != 1 {
		t.Fatalf("recovery not logged once: %s", output)
	}
}

func TestReloadStillAppliesWhenTheCacheCannotBeWritten(t *testing.T) {
	d, _, logs := reloadTestDaemon(t)
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), nil)}, errs: []error{nil}, commitErr: errors.New("disk full")}
	d.configurationReloader = reloader
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload reported a failure")
	}
	if d.hotConfiguration().digest != strings.Repeat("e", 64) || !strings.Contains(logs.String(), "disk full") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestReloadAppliesTheDeliveredTrustSetWithoutHostAcceptance(t *testing.T) {
	d, _, logs := reloadTestDaemon(t)
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	store, err := trustset.OpenStore(d.cfg.StateDir, trustTestRoots(root))
	if err != nil {
		t.Fatal(err)
	}
	d.agentPackTrust = &agentPackTrust{store: store}
	set := signTrustTestSet(t, root, 2, []trustTestSetKey{{id: "key-new", key: packKey}})
	reload := hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) { c.AgentPackTrustedKeySet = &set })
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{reload}, errs: []error{nil}}
	d.reloadConfiguration(context.Background())
	if _, ok := d.hotConfiguration().agentPackPublicKeys["key-new"]; ok {
		t.Fatal("a remotely delivered key became a hand-written trusted key")
	}
	if snapshot := store.Snapshot(); snapshot.HighestSequence != 2 || snapshot.Current == nil {
		t.Fatalf("the delivered trust set was not applied: %+v", snapshot)
	}
	foreign := signTrustTestSet(t, newTrustTestKey(t), 3, nil)
	next := hotReload(d.cfg, strings.Repeat("f", 64), func(c *config.Config) { c.AgentPackTrustedKeySet = &foreign })
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{next}, errs: []error{nil}}
	d.reloadConfiguration(context.Background())
	if snapshot := store.Snapshot(); snapshot.HighestSequence != 2 {
		t.Fatalf("an unverifiable delivered set replaced the trust state: %+v", snapshot)
	}
	if !strings.Contains(logs.String(), "signature_invalid") && !strings.Contains(logs.String(), "unknown_root") {
		t.Fatalf("the refused set was not reported: %s", logs.String())
	}
}

func TestReloadRemovingAllInstallationsClearsSourcePublicationServices(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), nil)}, errs: []error{nil}}
	d.reloadConfiguration(context.Background())
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("c", 64), func(c *config.Config) {
		c.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{}
	})}, errs: []error{nil}}
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload failed")
	}
	services := d.sourcePublicationServices()
	if services.publisher != nil || services.reviewerIdentityVerifier != nil || services.providerReviewFeedbackContentReader != nil {
		t.Fatalf("services = %+v", services)
	}
}

func TestAJobKeepsTheRepositoriesItStartedWith(t *testing.T) {
	jr := workspaceValidationRunner(t, []v1.JobRepository{{RepositoryID: "repo-9", Primary: true}})
	started := jr.d.hotConfiguration()
	jr.configuration = &started
	if err := jr.d.applyHotConfiguration(func() config.Config {
		next := jr.d.cfg
		next.Repositories = map[string]config.RepositoryConfig{"repo-9": {URL: "https://github.com/acme/late.git"}}
		return next
	}(), strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jr.prepareWorkspace(context.Background()); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("the running job saw a repository added after it started: %v", err)
	}
	if _, ok := jr.d.hotConfiguration().repositories["repo-9"]; !ok {
		t.Fatal("the daemon did not apply the new repository")
	}
}

func TestAJobVerifiesItsAgentPackAgainstTheKeysItStartedWith(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	started := d.hotConfiguration()
	if err := d.applyHotConfiguration(hotReload(d.cfg, strings.Repeat("e", 64), nil).Merged.Config, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	job := v1.JobSpec{AgentPack: &v1.AgentPackRef{KeyID: "key-2"}, AgentPackAuthority: &v1.SignedAgentPackAuthority{}}
	if err := d.verifyAgentPackAuthority(job, started.agentPackPublicKeys); err == nil || !strings.Contains(err.Error(), string(v1.CommandAckReasonAgentPackKeyUntrusted)) {
		t.Fatalf("a key delivered after the job snapshot was trusted: %v", err)
	}
	job.AgentPack.KeyID = "key-1"
	if err := d.verifyJobAgentPackAuthority(job); err == nil || !strings.Contains(err.Error(), string(v1.CommandAckReasonAgentPackKeyUntrusted)) {
		t.Fatalf("a key removed by the reload is still trusted: %v", err)
	}
}

func TestHotSwapsAreSafeAgainstConcurrentReaders(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	base := d.cfg
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				hot := d.hotConfiguration()
				if (hot.digest == strings.Repeat("d", 64)) != (hot.labels["pool"] == "eu-linux") {
					t.Error("a reader saw a torn configuration")
					return
				}
				_ = d.agentConfigScanRepositoriesFrom(hot.repositories)
				_ = d.sourcePublicationServices()
				_ = gitHubHTTPSOrigin("https://github.com/acme/api.git", hot.sourcePublicationInstallations)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_ = d.publishCapability(context.Background())
		}
	}()
	for index := range 200 {
		digest := strings.Repeat("e", 64)
		next := hotReload(base, digest, nil).Merged.Config
		if index%2 == 1 {
			digest = strings.Repeat("d", 64)
			next = base
		}
		if err := d.applyHotConfiguration(next, digest); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}

func TestConfigurationReloadDelayUsesJitterAndBacksOff(t *testing.T) {
	for _, tc := range []struct {
		failures int
		jitter   float64
		want     time.Duration
	}{
		{0, 0, 50 * time.Second},
		{0, 0.5, 60 * time.Second},
		{0, 1, 70 * time.Second},
		{1, 0.5, 60 * time.Second},
		{2, 0.5, 120 * time.Second},
		{3, 0.5, 240 * time.Second},
		{4, 0.5, 480 * time.Second},
		{5, 0.5, 600 * time.Second},
		{40, 0.5, 600 * time.Second},
		{40, 1, 610 * time.Second},
	} {
		if got := configurationReloadDelay(tc.failures, tc.jitter); got != tc.want {
			t.Fatalf("delay(%d, %v) = %s, want %s", tc.failures, tc.jitter, got, tc.want)
		}
	}
}

func TestConfigurationReloadLoopRunsUntilCancelled(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), nil), {Outcome: remoteconfig.ReloadUnchanged}}, errs: []error{nil, nil}}
	d.configurationReloader = reloader
	var delays []int
	var mu sync.Mutex
	d.configurationReloadWait = func(failures int) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		delays = append(delays, failures)
		return time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.configurationReloadLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for reloader.commits() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reload loop did not stop")
	}
	if reloader.commits() != 1 || d.hotConfiguration().digest != strings.Repeat("e", 64) {
		t.Fatal("the loop did not apply the change")
	}
}

func TestReloadLogsAFailedApplyOncePerStreak(t *testing.T) {
	d, _, logs := reloadTestDaemon(t)
	refused := hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) { c.Provider = "codex" })
	applied := hotReload(d.cfg, strings.Repeat("f", 64), nil)
	d.configurationReloader = &fakeConfigurationReloader{
		reloads: []remoteconfig.Reload{refused, refused, refused, applied, refused},
		errs:    []error{nil, nil, nil, nil, nil},
	}
	for attempt := range 3 {
		if d.reloadConfiguration(context.Background()) {
			t.Fatalf("attempt %d: a refused apply reported success", attempt)
		}
	}
	output := logs.String()
	if count := strings.Count(output, "level=ERROR msg=\"apply runner configuration"); count != 1 {
		t.Fatalf("errors in one streak = %d\n%s", count, output)
	}
	if count := strings.Count(output, "level=DEBUG msg=\"apply runner configuration failed again"); count != 2 {
		t.Fatalf("debug lines = %d\n%s", count, output)
	}
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("a valid apply reported a failure")
	}
	if d.reloadConfiguration(context.Background()) {
		t.Fatal("a refused apply reported success")
	}
	if count := strings.Count(logs.String(), "level=ERROR msg=\"apply runner configuration"); count != 2 {
		t.Fatalf("a new streak after a successful apply was not logged: %d\n%s", count, logs.String())
	}
}

func TestReloadReportsAFailedApplySoTheNextCheckIsUnconditional(t *testing.T) {
	d, events, _ := reloadTestDaemon(t)
	before := d.hotConfiguration()
	unbuildable := hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) {
		c.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "subversion"}}
	})
	secondGuard := hotReload(d.cfg, strings.Repeat("b", 64), func(c *config.Config) { c.Provider = "codex" })
	restart := remoteconfig.Reload{Outcome: remoteconfig.ReloadRestartRequired, Delivery: remoteconfig.Delivery{Digest: strings.Repeat("f", 64)}, RestartKeys: []string{"provider"}}
	applied := hotReload(d.cfg, strings.Repeat("c", 64), nil)
	reloader := &fakeConfigurationReloader{
		reloads: []remoteconfig.Reload{unbuildable, unbuildable, secondGuard, {}, restart, {Outcome: remoteconfig.ReloadUnchanged}, applied},
		errs:    []error{nil, nil, nil, &remoteconfig.StatusError{Status: 401, Code: "unauthorized"}, nil, nil, nil},
	}
	d.configurationReloader = reloader
	for attempt := range 3 {
		if d.reloadConfiguration(context.Background()) {
			t.Fatalf("attempt %d: a failed apply reported success", attempt)
		}
	}
	if !reflect.DeepEqual(d.hotConfiguration(), before) || reloader.commits() != 0 || len(publishedManifests(t, events)) != 0 {
		t.Fatal("a failed apply changed the running configuration")
	}
	want := []string{strings.Repeat("e", 64), strings.Repeat("e", 64), strings.Repeat("b", 64)}
	if got := reloader.refusals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("refusals = %v, want %v", got, want)
	}
	if d.reloadConfiguration(context.Background()) {
		t.Fatal("a 401 reported success")
	}
	for range 3 {
		d.reloadConfiguration(context.Background())
	}
	if got := reloader.refusals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a fetch failure, restart-required document, unchanged check or successful apply was reported as a failed apply: %v", got)
	}
	if reloader.commits() != 1 || d.hotConfiguration().digest != strings.Repeat("c", 64) {
		t.Fatal("the valid document was not applied")
	}
}
