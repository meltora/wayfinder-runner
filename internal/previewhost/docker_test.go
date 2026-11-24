package previewhost

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/redact"
)

const (
	testRunnerID  = "runner-1"
	testPreviewID = "preview-1"
	testCommitSHA = "0123456789abcdef0123456789abcdef01234567"
)

func testImage(name string) string {
	return "registry.example.com/" + name + "@sha256:" + strings.Repeat("a", 64)
}

func generated(generator string) v1.PreviewBlueprintEnvEntryV1 {
	return v1.PreviewBlueprintEnvEntryV1{Kind: v1.PreviewBlueprintEnvGenerated, Generator: generator}
}

func fake(value string) v1.PreviewBlueprintEnvEntryV1 {
	return v1.PreviewBlueprintEnvEntryV1{Kind: v1.PreviewBlueprintEnvFake, Value: value}
}

func testContent() v1.PreviewBlueprintContentV1 {
	return v1.PreviewBlueprintContentV1{
		SchemaVersion: 1,
		Services: []v1.PreviewBlueprintServiceV1{
			{
				Name: "db", Role: "database", Image: testImage("postgres"),
				Env: map[string]v1.PreviewBlueprintEnvEntryV1{
					"POSTGRES_PASSWORD": generated("random_secret"),
					"POSTGRES_USER":     fake("preview"),
				},
				Health: &v1.PreviewBlueprintServiceHealthV1{Command: []string{"pg_isready", "-U", "preview"}, IntervalSeconds: 1},
			},
			{Name: "cache", Role: "cache", Image: testImage("redis")},
			{Name: "mail", Role: "mail_sink", Image: testImage("mailpit")},
			{Name: "search", Role: "other", Image: testImage("search")},
		},
		App: v1.PreviewBlueprintAppV1{
			Image: testImage("node"), WorkingDirectory: "web",
			Install: []string{"npm ci"}, Build: []string{"npm run build"},
			Migrate: []string{"npm run migrate"}, Seed: []string{"npm run seed"},
			Start: "npm start", Port: 3000,
			HealthChecks: []v1.PreviewBlueprintHealthCheckV1{
				{ID: "root", Path: "/", ExpectedStatus: 200},
				{ID: "api", Path: "/api/health", ExpectedStatus: 204},
			},
			Env: map[string]v1.PreviewBlueprintEnvEntryV1{
				"DATABASE_URL":      generated("postgres_url"),
				"REDIS_URL":         generated("redis_url"),
				"SMTP_URL":          generated("smtp_url"),
				"PUBLIC_URL":        generated("preview_url"),
				"POSTGRES_PASSWORD": generated("random_secret"),
				"SESSION_SECRET":    generated("random_secret"),
				"FEATURE_FLAG":      fake("on"),
				"STRIPE_KEY":        {Kind: v1.PreviewBlueprintEnvSecret},
				"ANALYTICS_TOKEN":   {Kind: v1.PreviewBlueprintEnvDisabled},
			},
		},
		Limits: v1.PreviewBlueprintLimitsV1{MemoryMB: 512, CPUs: 1.5, MaxLifetimeMinutes: 60},
		Egress: v1.PreviewBlueprintEgressV1{Mode: v1.PreviewBlueprintEgressNone},
	}
}

func testOperation() v1.PreviewDeployOperation {
	now := time.Now().UTC().Truncate(time.Millisecond)
	operation := v1.PreviewDeployOperation{
		CommitSet:  []v1.PreviewObservationCommit{{RepositoryID: "repo-a", CommitSHA: testCommitSHA}},
		PreviewKey: strings.Repeat("a", 26),
		ExpiresAt:  now.Add(time.Hour),
	}
	operation.PreviewID = testPreviewID
	operation.LeaseExpiresAt = now.Add(45 * time.Minute)
	operation.Blueprint = v1.PreviewDeployBlueprint{BlueprintID: "blueprint-1", Version: "1", Digest: strings.Repeat("b", 64)}
	return operation
}

type harness struct {
	t          *testing.T
	engine     *fakeEngine
	workspaces *fakeWorkspaces
	server     *httptest.Server
	status     map[string]*atomic.Int32
	controller *Docker
	cfg        Config
	ownership  *fakeOwnership
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	h := &harness{
		t: t, engine: newFakeEngine(), workspaces: &fakeWorkspaces{},
		status: map[string]*atomic.Int32{"/": {}, "/api/health": {}},
	}
	h.status["/"].Store(200)
	h.status["/api/health"].Store(204)
	h.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		status, ok := h.status[request.URL.Path]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(h.server.Close)
	_, port, err := net.SplitHostPort(h.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	h.engine.hostPort, _ = strconv.Atoi(port)
	h.cfg = Config{
		RunnerID: testRunnerID, StateDir: t.TempDir(),
		Lease: 2 * time.Hour, HealthInterval: 10 * time.Millisecond,
		PhaseTimeouts: map[v1.PreviewDeployPhase]time.Duration{
			v1.PreviewDeployPhaseServices: 2 * time.Second,
			v1.PreviewDeployPhaseHealth:   2 * time.Second,
		},
		GatewayExecutable: staticRunnerFixture(t),
	}
	return h
}

func (h *harness) build() *Docker {
	h.t.Helper()
	deps := Dependencies{
		Engine: h.engine, Workspaces: h.workspaces,
		Repositories: fakeRepositories{"repo-a": {URL: "https://git.example.com/a.git"}, "repo-b": {URL: "https://git.example.com/b.git"}},
	}
	if h.ownership != nil {
		deps.Ownership = h.ownership
	}
	controller, err := New(h.cfg, deps)
	if err != nil {
		h.t.Fatal(err)
	}
	controller.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	h.controller = controller
	return controller
}

func (h *harness) deploy(ctx context.Context, content v1.PreviewBlueprintContentV1, operation v1.PreviewDeployOperation) v1.PreviewDeployOutcome {
	h.t.Helper()
	if h.controller == nil {
		h.build()
	}
	return h.controller.Deploy(ctx, DeployRequest{Operation: operation, Content: content}).Outcome
}

func (h *harness) live() Objects {
	h.t.Helper()
	objects, err := h.engine.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID))
	if err != nil {
		h.t.Fatal(err)
	}
	return objects
}

func specNamed(t *testing.T, specs []ContainerSpec, name string) ContainerSpec {
	t.Helper()
	for _, spec := range specs {
		if spec.Name == name {
			return spec
		}
	}
	t.Fatalf("no container %s in %v", name, specs)
	return ContainerSpec{}
}

func TestDeploySucceedsWithSortedHealthChecks(t *testing.T) {
	h := newHarness(t)
	outcome := h.deploy(context.Background(), testContent(), testOperation())
	if outcome.Kind != v1.PreviewDeployDeployed || outcome.Phase != "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(outcome.Checks) != 2 || outcome.Checks[0].CheckID != "api" || outcome.Checks[1].CheckID != "root" {
		t.Fatalf("checks = %+v", outcome.Checks)
	}
	for _, check := range outcome.Checks {
		if check.Status != v1.PreviewObservationCheckPassed || check.DurationMS > 120_000 {
			t.Fatalf("check = %+v", check)
		}
	}
	if outcome.Checks[0].HTTPStatus != 204 || outcome.Checks[1].HTTPStatus != 200 {
		t.Fatalf("checks = %+v", outcome.Checks)
	}
	if h.live().Empty() {
		t.Fatal("deployed preview has no live resources")
	}
}

func TestEveryCreatedObjectCarriesOnlyPreviewLabels(t *testing.T) {
	h := newHarness(t)
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	want := PreviewLabels(testRunnerID, testPreviewID)
	check := func(kind, name string, labels map[string]string) {
		for key, value := range want {
			if labels[key] != value {
				t.Fatalf("%s %s labels = %v, want %v", kind, name, labels, want)
			}
		}
		for key := range labels {
			if key == "com.ourplatform.runner-id" || key == "com.ourplatform.attempt-id" {
				t.Fatalf("%s %s carries job label %s", kind, name, key)
			}
		}
	}
	created := h.engine.Created()
	if len(created) < 10 {
		t.Fatalf("created = %d containers", len(created))
	}
	for _, spec := range created {
		check("container", spec.Name, spec.Labels)
	}
	networks := h.engine.CreatedNetworks()
	if len(networks) != 2 {
		t.Fatalf("networks = %+v", networks)
	}
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	for index, name := range []string{prefix, prefix + "-edge"} {
		check("network", networks[index].Name, networks[index].Labels)
		if networks[index].Name != name {
			t.Fatalf("network name = %q, want %q", networks[index].Name, name)
		}
	}
	for _, spec := range created {
		network := prefix
		if spec.Name == prefix+"-gateway" {
			network = prefix + "-edge"
		}
		if spec.Network != network || !strings.HasPrefix(spec.Name, prefix+"-") {
			t.Fatalf("container %s on network %q", spec.Name, spec.Network)
		}
	}
}

func TestResourceNamesAreOpaqueAndStable(t *testing.T) {
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	if !regexp.MustCompile(`^our-preview-[0-9a-f]{12}$`).MatchString(prefix) {
		t.Fatalf("prefix = %q", prefix)
	}
	if prefix != ResourcePrefix(testRunnerID, testPreviewID) || prefix == ResourcePrefix(testRunnerID, "preview-2") ||
		prefix == ResourcePrefix("runner-2", testPreviewID) {
		t.Fatal("prefix is not bound to runner and preview")
	}
}

func TestPreviewLabelsNeverIncludeJobLabels(t *testing.T) {
	labels := PreviewLabels(testRunnerID, testPreviewID)
	if len(labels) != 2 || labels[LabelPreviewHost] != testRunnerID || labels[LabelPreviewID] != testPreviewID {
		t.Fatalf("labels = %v", labels)
	}
	for key := range labels {
		if strings.Contains(key, "runner-id") || strings.Contains(key, "attempt-id") {
			t.Fatalf("labels = %v", labels)
		}
	}
}

func TestContainerPolicyPerRole(t *testing.T) {
	h := newHarness(t)
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	created := h.engine.Created()
	wantCaps := map[string][]string{
		prefix + "-svc-db":     {"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"},
		prefix + "-svc-cache":  {"SETGID", "SETUID"},
		prefix + "-svc-mail":   nil,
		prefix + "-svc-search": nil,
	}
	for name, caps := range wantCaps {
		spec := specNamed(t, created, name)
		if !slices.Equal(spec.CapAdd, caps) {
			t.Fatalf("%s cap-add = %v, want %v", name, spec.CapAdd, caps)
		}
		if spec.MemoryMB != 512 || spec.CPUs != 1.5 || spec.PidsLimit != 512 {
			t.Fatalf("%s limits = %d %v %d", name, spec.MemoryMB, spec.CPUs, spec.PidsLimit)
		}
		if spec.NetworkAlias != strings.TrimPrefix(name, prefix+"-svc-") {
			t.Fatalf("%s alias = %q", name, spec.NetworkAlias)
		}
	}
	for _, spec := range created {
		if _, service := wantCaps[spec.Name]; service || spec.Name == prefix+"-gateway" {
			continue
		}
		if len(spec.CapAdd) != 0 || !spec.ReadOnly || spec.User != "10001:10001" {
			t.Fatalf("app container %s policy = %+v", spec.Name, spec)
		}
		if !slices.Equal(spec.Tmpfs, []string{"/tmp:rw,nosuid,nodev,size=256m"}) || spec.WorkDir != "/workspace/web" {
			t.Fatalf("app container %s fs = %v %q", spec.Name, spec.Tmpfs, spec.WorkDir)
		}
		if len(spec.Mounts) != 1 || spec.Mounts[0].Target != "/workspace" || spec.Mounts[0].ReadOnly ||
			spec.Mounts[0].Source != filepath.Join(h.cfg.StateDir, "previews", strings.TrimPrefix(prefix, "our-preview-"), "workspace") {
			t.Fatalf("app container %s mounts = %+v", spec.Name, spec.Mounts)
		}
		if spec.MemoryMB != 512 || spec.CPUs != 1.5 || spec.PidsLimit != 512 || spec.Image != testImage("node") {
			t.Fatalf("app container %s = %+v", spec.Name, spec)
		}
	}
	app := specNamed(t, created, prefix+"-app")
	if app.PublishPort != 0 || app.NetworkAlias != "app" || !slices.Equal(app.Command, []string{"-c", "npm start"}) || app.Entrypoint != "/bin/sh" {
		t.Fatalf("app = %+v", app)
	}
}

func phaseIndex(t *testing.T, events []string, prefix string) int {
	t.Helper()
	for index, event := range events {
		if strings.HasPrefix(event, prefix) {
			return index
		}
	}
	t.Fatalf("no event %q in %v", prefix, events)
	return -1
}

func TestDeployRunsPhasesInOrder(t *testing.T) {
	h := newHarness(t)
	h.engine.present[testImage("redis")] = true
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	events := h.engine.Events()
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	order := []string{
		"pull " + testImage("postgres"), "network " + prefix, "detached " + prefix + "-gateway", "detached " + prefix + "-svc-db",
		"exec " + prefix + "-svc-db pg_isready -U preview", "once -c npm ci", "once -c npm run build",
		"once -c npm run migrate", "once -c npm run seed", "detached " + prefix + "-app",
	}
	previous := -1
	for _, event := range order {
		index := phaseIndex(t, events, event)
		if index <= previous {
			t.Fatalf("event %q out of order in %v", event, events)
		}
		previous = index
	}
	for _, event := range events {
		if event == "pull "+testImage("redis") {
			t.Fatal("present image was pulled again")
		}
	}
	if len(h.workspaces.calls) != 1 || h.workspaces.calls[0].SHA != testCommitSHA ||
		h.workspaces.calls[0].URL != "https://git.example.com/a.git" ||
		h.workspaces.calls[0].Dest != filepath.Join(h.cfg.StateDir, "previews", strings.TrimPrefix(prefix, "our-preview-"), "workspace") {
		t.Fatalf("checkout = %+v", h.workspaces.calls)
	}
}

func TestDeployChecksOutEachRepositoryInItsOwnDirectory(t *testing.T) {
	h := newHarness(t)
	operation := testOperation()
	operation.CommitSet = append(operation.CommitSet, v1.PreviewObservationCommit{RepositoryID: "repo-b", CommitSHA: strings.Repeat("f", 40)})
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	workspace := filepath.Join(h.cfg.StateDir, "previews", strings.TrimPrefix(ResourcePrefix(testRunnerID, testPreviewID), "our-preview-"), "workspace")
	if len(h.workspaces.calls) != 2 || h.workspaces.calls[0].Dest != filepath.Join(workspace, "repo-a") ||
		h.workspaces.calls[1].Dest != filepath.Join(workspace, "repo-b") || h.workspaces.calls[1].URL != "https://git.example.com/b.git" {
		t.Fatalf("checkout = %+v", h.workspaces.calls)
	}
}

func assertFailedAndTornDown(t *testing.T, h *harness, outcome v1.PreviewDeployOutcome, phase v1.PreviewDeployPhase) {
	t.Helper()
	if outcome.Kind != v1.PreviewDeployFailed || outcome.Phase != phase {
		t.Fatalf("outcome = %+v, want failed:%s", outcome, phase)
	}
	if !h.live().Empty() {
		t.Fatalf("resources survived failure: %+v", h.live())
	}
	dir := filepath.Join(h.cfg.StateDir, "previews", strings.TrimPrefix(ResourcePrefix(testRunnerID, testPreviewID), "our-preview-"))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("preview directory survived failure: %v", err)
	}
}

func TestDeployStopsAtFirstFailingCommandPhase(t *testing.T) {
	phases := []struct {
		phase   v1.PreviewDeployPhase
		command string
		later   []string
	}{
		{v1.PreviewDeployPhaseInstall, "npm ci", []string{"npm run build", "npm run migrate", "npm run seed"}},
		{v1.PreviewDeployPhaseBuild, "npm run build", []string{"npm run migrate", "npm run seed"}},
		{v1.PreviewDeployPhaseMigrate, "npm run migrate", []string{"npm run seed"}},
		{v1.PreviewDeployPhaseSeed, "npm run seed", nil},
	}
	for _, test := range phases {
		t.Run(string(test.phase), func(t *testing.T) {
			h := newHarness(t)
			content := testContent()
			switch test.phase {
			case v1.PreviewDeployPhaseInstall:
				content.App.Install = append(content.App.Install, "npm run postinstall")
				test.later = append(test.later, "npm run postinstall")
			}
			h.engine.runOnce = func(_ context.Context, spec ContainerSpec) (string, error) {
				if spec.Command[1] == test.command {
					return "boom in " + test.command, errors.New("exit status 1")
				}
				return "ok", nil
			}
			outcome := h.deploy(context.Background(), content, testOperation())
			assertFailedAndTornDown(t, h, outcome, test.phase)
			if !strings.Contains(outcome.LogTail, "boom in "+test.command) {
				t.Fatalf("log tail = %q", outcome.LogTail)
			}
			for _, event := range h.engine.Events() {
				for _, later := range test.later {
					if event == "once -c "+later {
						t.Fatalf("later command %q ran after failure", later)
					}
				}
				if strings.HasPrefix(event, "detached "+ResourcePrefix(testRunnerID, testPreviewID)+"-app") {
					t.Fatal("app started after failure")
				}
			}
		})
	}
}

func TestDeployFailurePhases(t *testing.T) {
	t.Run("checkout", func(t *testing.T) {
		h := newHarness(t)
		h.workspaces.err = errors.New("fetch failed")
		assertFailedAndTornDown(t, h, h.deploy(context.Background(), testContent(), testOperation()), v1.PreviewDeployPhaseCheckout)
		if len(h.engine.Events()) != 0 {
			t.Fatalf("events after checkout failure = %v", h.engine.Events())
		}
	})
	t.Run("unknown repository", func(t *testing.T) {
		h := newHarness(t)
		operation := testOperation()
		operation.CommitSet[0].RepositoryID = "repo-unknown"
		assertFailedAndTornDown(t, h, h.deploy(context.Background(), testContent(), operation), v1.PreviewDeployPhaseCheckout)
		if len(h.workspaces.calls) != 0 {
			t.Fatal("unknown repository was checked out")
		}
	})
	t.Run("pull", func(t *testing.T) {
		h := newHarness(t)
		h.engine.pullErr[testImage("redis")] = errors.New("manifest unknown")
		outcome := h.deploy(context.Background(), testContent(), testOperation())
		assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhasePull)
		if !strings.Contains(outcome.LogTail, "pull failed") {
			t.Fatalf("log tail = %q", outcome.LogTail)
		}
	})
	t.Run("pull refuses private registry address", func(t *testing.T) {
		h := newHarness(t)
		h.build().lookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.7")}, nil
		}
		assertFailedAndTornDown(t, h, h.deploy(context.Background(), testContent(), testOperation()), v1.PreviewDeployPhasePull)
		for _, event := range h.engine.Events() {
			if strings.HasPrefix(event, "pull ") {
				t.Fatalf("pulled from a registry that resolves to a private address: %v", h.engine.Events())
			}
		}
	})
	t.Run("services", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.PhaseTimeouts[v1.PreviewDeployPhaseServices] = 100 * time.Millisecond
		h.engine.exec = func(context.Context, string, []string) (string, error) {
			return "database not ready", errors.New("exit status 2")
		}
		outcome := h.deploy(context.Background(), testContent(), testOperation())
		assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseServices)
		if !strings.Contains(outcome.LogTail, "database not ready") {
			t.Fatalf("log tail = %q", outcome.LogTail)
		}
	})
	t.Run("start", func(t *testing.T) {
		h := newHarness(t)
		h.engine.detached = func(spec ContainerSpec) error {
			if strings.HasSuffix(spec.Name, "-app") {
				return errors.New("port allocation failed")
			}
			return nil
		}
		assertFailedAndTornDown(t, h, h.deploy(context.Background(), testContent(), testOperation()), v1.PreviewDeployPhaseStart)
	})
	t.Run("health", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.PhaseTimeouts[v1.PreviewDeployPhaseHealth] = 150 * time.Millisecond
		h.status["/api/health"].Store(500)
		outcome := h.deploy(context.Background(), testContent(), testOperation())
		assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseHealth)
		if len(outcome.Checks) != 2 || outcome.Checks[0].CheckID != "api" || outcome.Checks[0].Status != v1.PreviewObservationCheckFailed ||
			outcome.Checks[0].HTTPStatus != 500 || outcome.Checks[1].Status != v1.PreviewObservationCheckPassed {
			t.Fatalf("checks = %+v", outcome.Checks)
		}
		if !strings.Contains(outcome.LogTail, "logs of "+ResourcePrefix(testRunnerID, testPreviewID)+"-app") {
			t.Fatalf("log tail = %q", outcome.LogTail)
		}
	})
}

func TestDeployTeardownFailureIsUncertain(t *testing.T) {
	t.Run("remove error", func(t *testing.T) {
		h := newHarness(t)
		h.engine.runOnce = func(context.Context, ContainerSpec) (string, error) { return "", errors.New("exit status 1") }
		h.engine.removeErr = errors.New("daemon unavailable")
		outcome := h.deploy(context.Background(), testContent(), testOperation())
		if outcome.Kind != v1.PreviewDeployUncertain || outcome.Phase != "" || outcome.LogTail != "" || len(outcome.Checks) != 0 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("resources survive removal", func(t *testing.T) {
		h := newHarness(t)
		h.engine.runOnce = func(context.Context, ContainerSpec) (string, error) { return "", errors.New("exit status 1") }
		h.engine.sticky = true
		if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployUncertain {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
}

func TestDeployLeaseExpiryTearsDown(t *testing.T) {
	h := newHarness(t)
	h.engine.runOnce = func(ctx context.Context, spec ContainerSpec) (string, error) {
		if spec.Command[1] == "npm run build" {
			<-ctx.Done()
			return "interrupted", ctx.Err()
		}
		return "ok", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	outcome := h.deploy(ctx, testContent(), testOperation())
	assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseLeaseExpired)
	assertLeftoverRemoved(t, h, ResourcePrefix(testRunnerID, testPreviewID)+"-run-build-0")
}

func assertLeftoverRemoved(t *testing.T, h *harness, name string) {
	t.Helper()
	if !slices.Contains(h.engine.Leftovers(), name) {
		t.Fatalf("interrupted one-shot %s was not left behind by the engine: %v", name, h.engine.Leftovers())
	}
	if _, exists := h.engine.containers[name]; exists {
		t.Fatalf("interrupted one-shot %s survived teardown", name)
	}
}

func TestDeployShutdownReportsRealPhase(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.engine.runOnce = func(runCtx context.Context, spec ContainerSpec) (string, error) {
		if spec.Command[1] == "npm run build" {
			cancel()
			<-runCtx.Done()
			return "interrupted", runCtx.Err()
		}
		return "ok", nil
	}
	assertFailedAndTornDown(t, h, h.deploy(ctx, testContent(), testOperation()), v1.PreviewDeployPhaseBuild)
	assertLeftoverRemoved(t, h, ResourcePrefix(testRunnerID, testPreviewID)+"-run-build-0")
}

func TestDeployShutdownWithUnprovenTeardownIsUncertain(t *testing.T) {
	h := newHarness(t)
	h.engine.sticky = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.engine.runOnce = func(runCtx context.Context, _ ContainerSpec) (string, error) {
		cancel()
		<-runCtx.Done()
		return "", runCtx.Err()
	}
	if outcome := h.deploy(ctx, testContent(), testOperation()); outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestDeployFailureBeforeLeaseExpiryKeepsRealPhase(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	h.engine.runOnce = func(_ context.Context, spec ContainerSpec) (string, error) {
		if spec.Command[1] == "npm run build" {
			return "compile error", errors.New("exit status 1")
		}
		return "ok", nil
	}
	h.engine.removeHook = func() { <-ctx.Done() }
	outcome := h.deploy(ctx, testContent(), testOperation())
	h.engine.removeHook = nil
	assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseBuild)
}

func TestTeardownReclaimErrorIsUncertain(t *testing.T) {
	h := newHarness(t)
	h.ownership = &fakeOwnership{}
	h.engine.runOnce = func(context.Context, ContainerSpec) (string, error) { return "", errors.New("exit status 1") }
	operation := testOperation()
	h.build()
	h.ownership.reclaimErr = errors.New("chown refused")
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployUncertain ||
		outcome.Phase != "" || outcome.LogTail != "" {
		t.Fatalf("deploy outcome = %+v", outcome)
	}
	if len(h.ownership.handOffs) != 1 || len(h.ownership.reclaims) == 0 {
		t.Fatalf("ownership calls = %+v", h.ownership)
	}
	request := LifecycleRequest{Operation: lifecycleOperation(v1.PreviewLifecycleDestroy, operation.ExpiresAt, nil), Host: testHost(operation.ExpiresAt)}
	if outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome; outcome.Kind != v1.PreviewLifecycleUncertain {
		t.Fatalf("destroy outcome = %+v", outcome)
	}
	h.ownership.reclaimErr = nil
	if outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome; outcome.Kind != v1.PreviewLifecycleSucceeded {
		t.Fatalf("destroy after reclaim recovers = %+v", outcome)
	}
}

func TestHealthProbeStaysOnLoopback(t *testing.T) {
	for _, path := range []string{"@evil/x", "//evil/x"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.PhaseTimeouts[v1.PreviewDeployPhaseHealth] = 100 * time.Millisecond
			content := testContent()
			content.App.HealthChecks = []v1.PreviewBlueprintHealthCheckV1{{ID: "evil", Path: path, ExpectedStatus: 200}}
			var hosts []string
			var mu sync.Mutex
			h.build().client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				mu.Lock()
				hosts = append(hosts, request.URL.Host)
				mu.Unlock()
				return http.DefaultTransport.RoundTrip(request)
			})
			assertFailedAndTornDown(t, h, h.deploy(context.Background(), content, testOperation()), v1.PreviewDeployPhaseHealth)
			mu.Lock()
			defer mu.Unlock()
			for _, host := range hosts {
				if hostname, _, err := net.SplitHostPort(host); err != nil || hostname != "127.0.0.1" {
					t.Fatalf("probe reached %q", host)
				}
			}
			if len(hosts) != 0 {
				t.Fatalf("refused path %q was still probed: %v", path, hosts)
			}
		})
	}
}

func TestDeployLeaseExpiryDuringHealthIsLeaseExpired(t *testing.T) {
	h := newHarness(t)
	h.build().client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	assertFailedAndTornDown(t, h, h.deploy(ctx, testContent(), testOperation()), v1.PreviewDeployPhaseLeaseExpired)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestDeployLeaseExpiryWithUnprovenTeardownIsUncertain(t *testing.T) {
	h := newHarness(t)
	h.engine.sticky = true
	h.engine.runOnce = func(ctx context.Context, _ ContainerSpec) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if outcome := h.deploy(ctx, testContent(), testOperation()); outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestPhaseTimeoutIsNotLeaseExpiry(t *testing.T) {
	h := newHarness(t)
	h.cfg.PhaseTimeouts[v1.PreviewDeployPhaseInstall] = 50 * time.Millisecond
	h.engine.runOnce = func(ctx context.Context, spec ContainerSpec) (string, error) {
		if spec.Command[1] == "npm ci" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "ok", nil
	}
	assertFailedAndTornDown(t, h, h.deploy(context.Background(), testContent(), testOperation()), v1.PreviewDeployPhaseInstall)
	assertLeftoverRemoved(t, h, ResourcePrefix(testRunnerID, testPreviewID)+"-run-install-0")
}

func TestDeployLogTailIsRedactedAndBounded(t *testing.T) {
	h := newHarness(t)
	var secret string
	h.engine.runOnce = func(_ context.Context, spec ContainerSpec) (string, error) {
		secret = spec.Env["SESSION_SECRET"]
		return strings.Repeat("line\n", 1000) + "leaked " + secret + "\n", errors.New("exit status 1")
	}
	outcome := h.deploy(context.Background(), testContent(), testOperation())
	if outcome.Kind != v1.PreviewDeployFailed || secret == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if strings.Contains(outcome.LogTail, secret) || !strings.Contains(outcome.LogTail, "leaked ") {
		t.Fatalf("log tail leaks secret: %q", outcome.LogTail)
	}
	if len(outcome.LogTail) > v1.PreviewDeployLogTailMaxBytes || strings.Count(outcome.LogTail, "\n") > v1.PreviewDeployLogTailMaxLines {
		t.Fatalf("log tail is unbounded: %d bytes", len(outcome.LogTail))
	}
}

func TestDeployRemovesLeftoversBeforeStarting(t *testing.T) {
	h := newHarness(t)
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	h.engine.containers[prefix+"-app"] = ContainerSpec{Name: prefix + "-app", Labels: PreviewLabels(testRunnerID, testPreviewID)}
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if h.engine.Events()[0] != "remove" {
		t.Fatalf("events = %v", h.engine.Events())
	}
}

func TestCapability(t *testing.T) {
	h := newHarness(t)
	h.cfg.MaxConcurrent = 5
	h.cfg.MaxExtension = 48 * time.Hour
	capability := h.build().Capability()
	if capability.ProtocolVersion != 1 || capability.MaxConcurrent != 5 || capability.MaxExtensionMS == nil ||
		*capability.MaxExtensionMS != uint64((48*time.Hour).Milliseconds()) {
		t.Fatalf("capability = %+v", capability)
	}
	want := []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"}
	if !slices.Equal(capability.Actions, want) {
		t.Fatalf("actions = %v", capability.Actions)
	}
}

func lifecycleOperation(action v1.PreviewLifecycleAction, current time.Time, desired *time.Time) v1.PreviewLifecycleOperation {
	operation := v1.PreviewLifecycleOperation{
		PreviewID:         testPreviewID,
		Blueprint:         v1.PreviewObservationBlueprint{BlueprintID: "blueprint-1", Version: "1", Digest: strings.Repeat("b", 64)},
		CommitSet:         []v1.PreviewObservationCommit{{RepositoryID: "repo-a", CommitSHA: testCommitSHA}},
		OriginalExpiresAt: current, CurrentEffectiveExpiresAt: current, DesiredExpiresAt: desired,
	}
	operation.Action = action
	return operation
}

func testHost(expiresAt time.Time) journal.PreviewHost {
	return journal.PreviewHost{PreviewID: testPreviewID, ExpiresAt: expiresAt, OriginalExpiresAt: expiresAt, State: journal.PreviewHostDeployed}
}

func TestExtendIsIdempotentAndRequiresRunningApp(t *testing.T) {
	h := newHarness(t)
	operation := testOperation()
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	desired := operation.ExpiresAt.Add(2 * time.Hour)
	request := LifecycleRequest{Operation: lifecycleOperation(v1.PreviewLifecycleExtend, operation.ExpiresAt, &desired), Host: testHost(operation.ExpiresAt)}
	for attempt := 0; attempt < 2; attempt++ {
		outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome
		state := outcome.ResourceState
		if outcome.Kind != v1.PreviewLifecycleSucceeded || state == nil || state.Kind != v1.PreviewLifecycleResourceActive ||
			state.ExpiresAt == nil || !state.ExpiresAt.Equal(desired) || state.Blueprint == nil ||
			*state.Blueprint != request.Operation.Blueprint || !slices.Equal(state.CommitSet, request.Operation.CommitSet) ||
			state.Disposition != "" || state.PreviewURL != "" {
			t.Fatalf("attempt %d outcome = %+v state = %+v", attempt, outcome, state)
		}
	}
	tooFar := operation.ExpiresAt.Add(8 * 24 * time.Hour)
	far := request
	far.Operation = lifecycleOperation(v1.PreviewLifecycleExtend, operation.ExpiresAt, &tooFar)
	if outcome := h.controller.ApplyLifecycle(context.Background(), far).Outcome; outcome.Kind != v1.PreviewLifecyclePermanent ||
		outcome.Code != v1.PreviewLifecyclePolicyDenied {
		t.Fatalf("extension past maximum = %+v", outcome)
	}
	h.engine.stopped[ResourcePrefix(testRunnerID, testPreviewID)+"-app"] = true
	if outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome; outcome.Kind != v1.PreviewLifecyclePermanent ||
		outcome.Code != v1.PreviewLifecycleResourceNotFound {
		t.Fatalf("extend of stopped app = %+v", outcome)
	}
}

func TestDestroyIsIdempotentAndReportsAlreadyAbsent(t *testing.T) {
	h := newHarness(t)
	operation := testOperation()
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	request := LifecycleRequest{Operation: lifecycleOperation(v1.PreviewLifecycleDestroy, operation.ExpiresAt, nil), Host: testHost(operation.ExpiresAt)}
	first := h.controller.ApplyLifecycle(context.Background(), request).Outcome
	if first.Kind != v1.PreviewLifecycleSucceeded || first.ResourceState == nil ||
		!reflect.DeepEqual(*first.ResourceState, v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed}) {
		t.Fatalf("first destroy = %+v", first)
	}
	if !h.live().Empty() {
		t.Fatalf("resources survived destroy: %+v", h.live())
	}
	for attempt := 0; attempt < 2; attempt++ {
		again := h.controller.ApplyLifecycle(context.Background(), request).Outcome
		if again.Kind != v1.PreviewLifecycleSucceeded || again.ResourceState == nil ||
			!reflect.DeepEqual(*again.ResourceState, v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleAlreadyAbsent}) {
			t.Fatalf("repeated destroy = %+v", again)
		}
	}
	desired := operation.ExpiresAt.Add(time.Hour)
	extend := LifecycleRequest{Operation: lifecycleOperation(v1.PreviewLifecycleExtend, operation.ExpiresAt, &desired), Host: request.Host}
	if outcome := h.controller.ApplyLifecycle(context.Background(), extend).Outcome; outcome.Kind != v1.PreviewLifecyclePermanent ||
		outcome.Code != v1.PreviewLifecycleResourceNotFound {
		t.Fatalf("extend after destroy = %+v", outcome)
	}
}

func TestDestroyWithUnprovenTeardownIsUncertain(t *testing.T) {
	h := newHarness(t)
	operation := testOperation()
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	h.engine.sticky = true
	request := LifecycleRequest{Operation: lifecycleOperation(v1.PreviewLifecycleDestroy, operation.ExpiresAt, nil), Host: testHost(operation.ExpiresAt)}
	outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleReceiptUnavailable || outcome.ResourceState != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	h.engine.sticky = false
	h.engine.listErr = errors.New("daemon unavailable")
	if outcome := h.controller.ApplyLifecycle(context.Background(), request).Outcome; outcome.Kind != v1.PreviewLifecycleUncertain {
		t.Fatalf("outcome with list failure = %+v", outcome)
	}
}

func TestRecoverTearsDownLabelledResources(t *testing.T) {
	h := newHarness(t)
	operation := testOperation()
	if outcome := h.deploy(context.Background(), testContent(), operation); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	other := ResourcePrefix(testRunnerID, "preview-2") + "-app"
	h.engine.containers[other] = ContainerSpec{Name: other, Labels: PreviewLabels(testRunnerID, "preview-2")}
	h.engine.sticky = true
	if err := h.controller.Recover(context.Background(), testHost(operation.ExpiresAt)); err == nil {
		t.Fatal("recover claimed success with surviving resources")
	}
	h.engine.sticky = false
	if err := h.controller.Recover(context.Background(), testHost(operation.ExpiresAt)); err != nil {
		t.Fatal(err)
	}
	if !h.live().Empty() {
		t.Fatalf("resources survived recover: %+v", h.live())
	}
	if _, exists := h.engine.containers[other]; !exists {
		t.Fatal("recover removed another preview's container")
	}
	if err := h.controller.Recover(context.Background(), testHost(operation.ExpiresAt)); err != nil {
		t.Fatalf("recover of an absent preview: %v", err)
	}
}

func uniformPhaseTimeouts(timeout time.Duration) map[v1.PreviewDeployPhase]time.Duration {
	timeouts := map[v1.PreviewDeployPhase]time.Duration{}
	for _, phase := range deployPhases {
		timeouts[phase] = timeout
	}
	return timeouts
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	fits, err := Config{RunnerID: testRunnerID, StateDir: "/var/lib/runner", Lease: 20 * time.Minute,
		TeardownTimeout: 5*time.Minute + 30*time.Second - logGrace/2 - cancelledDockerCalls*dockerWaitDelay/2, PhaseTimeouts: uniformPhaseTimeouts(time.Minute)}.normalized()
	if err != nil || fits.Lease != 20*time.Minute {
		t.Fatalf("budget that fits the lease refused: %v", err)
	}
	cfg, err := Config{RunnerID: testRunnerID, StateDir: "/var/lib/runner"}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Lease != 45*time.Minute || cfg.MaxConcurrent != 3 || cfg.MaxExtension != 7*24*time.Hour ||
		cfg.JobUID != 10001 || cfg.JobGID != 10001 {
		t.Fatalf("defaults = %+v", cfg)
	}
	var sum time.Duration
	for _, phase := range deployPhases {
		if cfg.PhaseTimeouts[phase] <= 0 {
			t.Fatalf("phase %s has no timeout", phase)
		}
		sum += cfg.PhaseTimeouts[phase]
	}
	if _, err := (Config{RunnerID: testRunnerID, StateDir: "/var/lib/runner", Lease: 20 * time.Minute,
		TeardownTimeout: 5*time.Minute + 30*time.Second - logGrace/2 - cancelledDockerCalls*dockerWaitDelay/2 + time.Second,
		PhaseTimeouts:   uniformPhaseTimeouts(time.Minute)}).normalized(); err == nil {
		t.Fatal("budget that ignores docker wait delays accepted")
	}
	if budget := DeployBudget(sum, cfg.TeardownTimeout); budget > cfg.Lease {
		t.Fatalf("default deploy budget %s exceeds lease %s", budget, cfg.Lease)
	}
	for name, bad := range map[string]Config{
		"no runner":    {StateDir: "/var/lib/runner"},
		"relative dir": {RunnerID: testRunnerID, StateDir: "state"},
		"over lease": {RunnerID: testRunnerID, StateDir: "/var/lib/runner", Lease: time.Minute,
			PhaseTimeouts: map[v1.PreviewDeployPhase]time.Duration{v1.PreviewDeployPhaseInstall: 2 * time.Minute}},
		"negative": {RunnerID: testRunnerID, StateDir: "/var/lib/runner",
			PhaseTimeouts: map[v1.PreviewDeployPhase]time.Duration{v1.PreviewDeployPhaseBuild: -time.Second}},
		"unknown phase": {RunnerID: testRunnerID, StateDir: "/var/lib/runner",
			PhaseTimeouts: map[v1.PreviewDeployPhase]time.Duration{v1.PreviewDeployPhaseLeaseExpired: time.Second}},
		"root user":        {RunnerID: testRunnerID, StateDir: "/var/lib/runner", JobUID: -1},
		"relative gateway": {RunnerID: testRunnerID, StateDir: "/var/lib/runner", GatewayExecutable: "bin/our-runner"},
		"teardown over lease": {RunnerID: testRunnerID, StateDir: "/var/lib/runner", Lease: 20 * time.Minute,
			TeardownTimeout: 6 * time.Minute, PhaseTimeouts: uniformPhaseTimeouts(time.Minute)},
	} {
		if _, err := bad.normalized(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := New(Config{RunnerID: testRunnerID, StateDir: "/var/lib/runner"}, Dependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}
