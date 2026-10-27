package remoteconfig

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/mockcp"
)

type conditionalScript struct {
	responses []conditionalResponse
	digests   []string
}

type conditionalResponse struct {
	raw []byte
	err error
}

func (s *conditionalScript) fetch(_ context.Context, runningDigest string) ([]byte, error) {
	s.digests = append(s.digests, runningDigest)
	next := s.responses[0]
	if len(s.responses) > 1 {
		s.responses = s.responses[1:]
	}
	return next.raw, next.err
}

func bodyWithDigest(config map[string]any, digest string, keys ...testPackKey) map[string]any {
	body := deliveryBody(config, keys...)
	body["digest"] = digest
	return body
}

func runningReloader(t *testing.T, local Local, running map[string]any, fetch ConditionalFetchFunc) *Reloader {
	t.Helper()
	delivery := parsed(t, running)
	return NewReloader(local.StateDir, testRunnerID, local, delivery, fetch)
}

func TestReloaderSendsTheRunningDigestAndTreatsNotModifiedAsUnchanged(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	script := &conditionalScript{responses: []conditionalResponse{{err: ErrNotModified}}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if !reflect.DeepEqual(script.digests, []string{strings.Repeat("d", 64)}) || reloader.RunningDigest() != strings.Repeat("d", 64) {
		t.Fatalf("digests = %v", script.digests)
	}
}

func TestReloaderTreatsAFullResponseWithTheRunningDigestAsUnchanged(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, deliveryBody(dockerConfig()))}}}
	reload, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background())
	if err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
}

func TestReloaderAppliesADocumentThatChangesOnlyHotKeys(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	accepted := newTestPackKey(t, "key-accepted")
	awaiting := newTestPackKey(t, "key-new")
	next := dockerConfig(accepted, awaiting)
	next["labels"] = map[string]any{"pool": "us-linux"}
	next["repositories"] = map[string]any{"repo-9": map[string]any{"url": "https://github.com/acme/new.git"}}
	next["source_publication_installations"] = map[string]any{"installation-2": map[string]any{"provider": "github"}}
	raw := encode(t, bodyWithDigest(next, strings.Repeat("e", 64), accepted, awaiting))
	script := &conditionalScript{responses: []conditionalResponse{{raw: raw}}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	cfg := reload.Merged.Config
	if cfg.Labels["pool"] != "us-linux" || cfg.Repositories["repo-9"].URL != "https://github.com/acme/new.git" || len(cfg.Repositories) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	if _, ok := cfg.SourcePublicationInstallations["installation-2"]; !ok || len(cfg.SourcePublicationInstallations) != 1 {
		t.Fatalf("installations = %+v", cfg.SourcePublicationInstallations)
	}
	if len(cfg.AgentPackPublicKeys) != 0 {
		t.Fatalf("a remote pack key reached the configuration: %v", cfg.AgentPackPublicKeys)
	}
	if reloader.RunningDigest() != strings.Repeat("d", 64) {
		t.Fatal("the running digest moved before commit")
	}
	if err := reloader.Commit(reload); err != nil {
		t.Fatal(err)
	}
	if reloader.RunningDigest() != strings.Repeat("e", 64) {
		t.Fatalf("running digest = %s", reloader.RunningDigest())
	}
	stored, err := LoadCachedDelivery(local.StateDir)
	if err != nil || string(stored) != string(raw) {
		t.Fatal("the committed document was not cached")
	}
}

func TestReloaderRefusesEveryRestartRequiredKeyChange(t *testing.T) {
	changes := map[string]func(map[string]any){
		"provider":  func(c map[string]any) { c["provider"] = "codex" },
		"job_image": func(c map[string]any) { c["job_image"] = strings.Replace(testJobImage, "aaaa", "bbbb", 1) },
		"job_network": func(c map[string]any) {
			c["job_network"] = "restricted"
			c["job_network_name"] = "our-runner-jobs-dev"
			c["egress_policy_id"] = "policy"
		},
		"job_memory_mb": func(c map[string]any) { c["job_memory_mb"] = 8192 },
		"job_cpus":      func(c map[string]any) { c["job_cpus"] = 4 },
		"job_pids":      func(c map[string]any) { c["job_pids"] = 1024 },
	}
	expectedKeys := map[string][]string{"job_network": {"job_network", "job_network_name", "egress_policy_id"}}
	for key, change := range changes {
		want, ok := expectedKeys[key]
		if !ok {
			want = []string{key}
		}
		cached := encode(t, deliveryBody(dockerConfig()))
		local := configuredState(t, cached)
		next := dockerConfig()
		change(next)
		next["labels"] = map[string]any{"pool": "changed-too"}
		script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("f", 64)))}}}
		reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
		reload, err := reloader.Check(context.Background())
		if err != nil || reload.Outcome != ReloadRestartRequired || !reflect.DeepEqual(reload.RestartKeys, want) {
			t.Fatalf("%s: reload = %+v err = %v", key, reload, err)
		}
		if reload.Delivery.Digest != strings.Repeat("f", 64) || reloader.RunningDigest() != strings.Repeat("d", 64) {
			t.Fatalf("%s: digests moved", key)
		}
		stored, _ := LoadCachedDelivery(local.StateDir)
		if string(stored) != string(cached) {
			t.Fatalf("%s: a restart-required document replaced the cache", key)
		}
	}
}

func TestReloaderRefusesAnIsolationSwitchEitherWay(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	local.AcknowledgeDirect = true
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(directConfig(), strings.Repeat("f", 64)))}}}
	reload, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background())
	if err != nil || reload.Outcome != ReloadRestartRequired {
		t.Fatalf("docker to direct: reload = %+v err = %v", reload, err)
	}
	for _, key := range []string{"isolation", "allow_unsafe_direct", "provider", "job_image", "job_network", "job_memory_mb", "job_cpus", "job_pids"} {
		found := false
		for _, changed := range reload.RestartKeys {
			found = found || changed == key
		}
		if !found {
			t.Fatalf("restart keys = %v, missing %s", reload.RestartKeys, key)
		}
	}
	script = &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(dockerConfig(), strings.Repeat("f", 64)))}}}
	reload, err = runningReloader(t, local, deliveryBody(directConfig()), script.fetch).Check(context.Background())
	if err != nil || reload.Outcome != ReloadRestartRequired {
		t.Fatalf("direct to docker: reload = %+v err = %v", reload, err)
	}
}

func TestReloaderKeepsTheRunningDocumentOnFailures(t *testing.T) {
	failures := []error{
		&StatusError{Status: http.StatusUnauthorized, Code: "unauthorized"},
		&IdentityRefusedError{Err: errors.New("certificate expired")},
		&StatusError{Status: http.StatusForbidden},
		&StatusError{Status: http.StatusConflict, Code: "direct_isolation_unavailable"},
		&StatusError{Status: http.StatusNotFound, Code: "configuration_not_ready"},
		&StatusError{Status: http.StatusTooManyRequests, Code: "rate_limited"},
		&NetworkError{Err: errors.New("connection refused")},
	}
	for _, failure := range failures {
		cached := encode(t, deliveryBody(dockerConfig()))
		local := configuredState(t, cached)
		script := &conditionalScript{responses: []conditionalResponse{{err: failure}}}
		reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
		reload, err := reloader.Check(context.Background())
		if !errors.Is(err, failure) || reload.Outcome != ReloadUnchanged {
			t.Fatalf("%v: reload = %+v err = %v", failure, reload, err)
		}
		stored, _ := LoadCachedDelivery(local.StateDir)
		if string(stored) != string(cached) || reloader.RunningDigest() != strings.Repeat("d", 64) {
			t.Fatalf("%v: running document changed", failure)
		}
	}
	for _, raw := range [][]byte{[]byte(`{"unknown":1}`), encode(t, func() map[string]any {
		body := bodyWithDigest(dockerConfig(), strings.Repeat("f", 64))
		body["runner_id"] = "runner-other"
		return body
	}())} {
		local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
		script := &conditionalScript{responses: []conditionalResponse{{raw: raw}}}
		if _, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background()); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestReloaderRefusesADocumentWithAnInvalidRepositoryURL(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	next := dockerConfig()
	next["repositories"] = map[string]any{"repo-1": map[string]any{"url": "ssh://"}}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("f", 64)))}}}
	reload, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background())
	if err == nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
}

func TestReloaderOverMutualTLSWithTheMockControlPlane(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, runnerID := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	running := bodyWithDigest(dockerConfig(), strings.Repeat("a", 64))
	running["runner_id"] = runnerID
	delivery, err := ParseDelivery(encode(t, running), runnerID)
	if err != nil {
		t.Fatal(err)
	}
	local := Local{ControlPlaneURL: server.WSURL(), StateDir: stateDir}
	reloader := NewReloader(stateDir, runnerID, local, delivery, fetcher.FetchIfChanged)
	hot := dockerConfig()
	hot["labels"] = map[string]any{"pool": "hot"}
	hotBody := bodyWithDigest(hot, strings.Repeat("b", 64))
	hotBody["runner_id"] = runnerID
	restart := dockerConfig()
	restart["provider"] = "codex"
	restartBody := bodyWithDigest(restart, strings.Repeat("c", 64))
	restartBody["runner_id"] = runnerID
	server.SetConfigurationResponses(
		mockcp.ConfigurationResponse{Status: http.StatusNotModified},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: encode(t, hotBody)},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: encode(t, restartBody)},
		mockcp.ConfigurationResponse{Status: http.StatusUnauthorized, Body: []byte(`{"code":"unauthorized"}`)},
	)
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("304: %+v %v", reload, err)
	}
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply || reload.Merged.Config.Labels["pool"] != "hot" {
		t.Fatalf("hot: %+v %v", reload, err)
	}
	if err := reloader.Commit(reload); err != nil {
		t.Fatal(err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadRestartRequired {
		t.Fatalf("restart: %+v %v", reload, err)
	}
	if _, err := reloader.Check(context.Background()); !IsStatus(err, http.StatusUnauthorized, "unauthorized") {
		t.Fatalf("401: %v", err)
	}
	requests := server.ConfigurationRequests()
	want := []string{`"` + strings.Repeat("a", 64) + `"`, `"` + strings.Repeat("a", 64) + `"`, `"` + strings.Repeat("b", 64) + `"`, `"` + strings.Repeat("b", 64) + `"`}
	if len(requests) != len(want) {
		t.Fatalf("requests = %+v", requests)
	}
	for index, request := range requests {
		if request.IfNoneMatch != want[index] || request.ClientName != runnerID {
			t.Fatalf("request %d = %+v", index, request)
		}
	}
	server.Close()
	if _, err := reloader.Check(context.Background()); !IsTransient(err) {
		t.Fatalf("network: %v", err)
	}
	if reloader.RunningDigest() != strings.Repeat("b", 64) {
		t.Fatalf("running digest = %s", reloader.RunningDigest())
	}
}

func TestReloaderStopsSendingTheRunningDigestAfterAFailedApplyUntilOneSucceeds(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, runnerID := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	withRunner := func(config map[string]any, digest string) []byte {
		body := bodyWithDigest(config, digest)
		body["runner_id"] = runnerID
		return encode(t, body)
	}
	running, err := ParseDelivery(withRunner(dockerConfig(), strings.Repeat("a", 64)), runnerID)
	if err != nil {
		t.Fatal(err)
	}
	reloader := NewReloader(stateDir, runnerID, Local{ControlPlaneURL: server.WSURL(), StateDir: stateDir}, running, fetcher.FetchIfChanged)
	failing := dockerConfig()
	failing["source_publication_installations"] = map[string]any{"installation-1": map[string]any{"provider": "github"}}
	applies := dockerConfig()
	applies["labels"] = map[string]any{"pool": "applies"}
	invalid := dockerConfig()
	invalid["repositories"] = map[string]any{"repo-1": map[string]any{"url": "ssh://"}}
	server.SetConfigurationResponses(
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(failing, strings.Repeat("b", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(failing, strings.Repeat("b", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(applies, strings.Repeat("c", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusNotModified},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(invalid, strings.Repeat("e", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusUnauthorized, Body: []byte(`{"code":"unauthorized"}`)},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(invalid, strings.Repeat("e", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: withRunner(applies, strings.Repeat("c", 64))},
		mockcp.ConfigurationResponse{Status: http.StatusUnauthorized, Body: []byte(`{"code":"unauthorized"}`)},
		mockcp.ConfigurationResponse{Status: http.StatusNotModified},
	)
	for attempt := range 2 {
		reload, err := reloader.Check(context.Background())
		if err != nil || reload.Outcome != ReloadApply || reload.Delivery.Digest != strings.Repeat("b", 64) {
			t.Fatalf("failing apply %d: %+v %v", attempt, reload, err)
		}
		reloader.Refuse(reload)
	}
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply || reload.Merged.Config.Labels["pool"] != "applies" {
		t.Fatalf("applies: %+v %v", reload, err)
	}
	if err := reloader.Commit(reload); err != nil {
		t.Fatal(err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("304 after the apply: %+v %v", reload, err)
	}
	if _, err := reloader.Check(context.Background()); err == nil {
		t.Fatal("a document with an invalid repository URL was accepted")
	}
	if _, err := reloader.Check(context.Background()); !IsStatus(err, http.StatusUnauthorized, "unauthorized") {
		t.Fatalf("401 after an unreadable document: %v", err)
	}
	if _, err := reloader.Check(context.Background()); err == nil {
		t.Fatal("a document with an invalid repository URL was accepted on retry")
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("the running document again: %+v %v", reload, err)
	}
	if _, err := reloader.Check(context.Background()); !IsStatus(err, http.StatusUnauthorized, "unauthorized") {
		t.Fatalf("401 while conditional: %v", err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("304 after a 401: %+v %v", reload, err)
	}
	quoted := func(seed string) string { return `"` + strings.Repeat(seed, 64) + `"` }
	want := []string{quoted("a"), "", "", quoted("c"), quoted("c"), "", "", "", quoted("c"), quoted("c")}
	requests := server.ConfigurationRequests()
	if len(requests) != len(want) {
		t.Fatalf("requests = %+v", requests)
	}
	for index, request := range requests {
		if request.IfNoneMatch != want[index] {
			t.Fatalf("request %d If-None-Match = %q, want %q", index, request.IfNoneMatch, want[index])
		}
	}
	if reloader.RunningDigest() != strings.Repeat("c", 64) {
		t.Fatalf("running digest = %s", reloader.RunningDigest())
	}
}

func TestReloaderFetchesUnconditionallyAfterAnUnreadableDocument(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	restart := dockerConfig()
	restart["provider"] = "codex"
	script := &conditionalScript{responses: []conditionalResponse{
		{raw: []byte(`{"unknown":1}`)},
		{err: &NetworkError{Err: errors.New("connection refused")}},
		{raw: encode(t, bodyWithDigest(restart, strings.Repeat("f", 64)))},
		{raw: encode(t, deliveryBody(dockerConfig()))},
		{raw: encode(t, bodyWithDigest(restart, strings.Repeat("f", 64)))},
		{err: ErrNotModified},
	}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	if _, err := reloader.Check(context.Background()); err == nil {
		t.Fatal("an unreadable document was accepted")
	}
	if _, err := reloader.Check(context.Background()); !IsTransient(err) {
		t.Fatalf("network: %v", err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadRestartRequired {
		t.Fatalf("restart required while unconditional: %+v %v", reload, err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("the running document: %+v %v", reload, err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadRestartRequired {
		t.Fatalf("restart required while conditional: %+v %v", reload, err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("304: %+v %v", reload, err)
	}
	running := strings.Repeat("d", 64)
	if want := []string{running, "", "", "", running, running}; !reflect.DeepEqual(script.digests, want) {
		t.Fatalf("digests = %q, want %q", script.digests, want)
	}
}

func TestReloaderFetchesUnconditionallyAfterADocumentThatFailsToMerge(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(directConfig())))
	local.AcknowledgeDirect = false
	next := directConfig()
	next["labels"] = map[string]any{"pool": "next"}
	nextRaw := encode(t, bodyWithDigest(next, strings.Repeat("f", 64)))
	if _, err := ParseDelivery(nextRaw, testRunnerID); err != nil {
		t.Fatalf("the next document must parse: %v", err)
	}
	script := &conditionalScript{responses: []conditionalResponse{
		{raw: nextRaw},
		{raw: nextRaw},
		{raw: encode(t, deliveryBody(directConfig()))},
		{err: ErrNotModified},
	}}
	reloader := runningReloader(t, local, deliveryBody(directConfig()), script.fetch)
	for attempt := range 2 {
		reload, err := reloader.Check(context.Background())
		if !errors.Is(err, ErrDirectNotAcknowledged) || reload.Outcome != ReloadUnchanged {
			t.Fatalf("merge failure %d: reload = %+v err = %v", attempt, reload, err)
		}
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("the running document: %+v %v", reload, err)
	}
	if reload, err := reloader.Check(context.Background()); err != nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("304: %+v %v", reload, err)
	}
	running := strings.Repeat("d", 64)
	if want := []string{running, "", "", running}; !reflect.DeepEqual(script.digests, want) {
		t.Fatalf("digests = %q, want %q", script.digests, want)
	}
	if reloader.RunningDigest() != running {
		t.Fatalf("running digest = %s", reloader.RunningDigest())
	}
}

func TestANewReloaderStartsConditional(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	script := &conditionalScript{responses: []conditionalResponse{{raw: []byte(`{"unknown":1}`)}, {err: ErrNotModified}}}
	if _, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background()); err == nil {
		t.Fatal("an unreadable document was accepted")
	}
	if _, err := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{strings.Repeat("d", 64), strings.Repeat("d", 64)}; !reflect.DeepEqual(script.digests, want) {
		t.Fatalf("digests = %q", script.digests)
	}
}

func TestReloaderAppliesARevokedDigestChangeHotAndARestartReappliesItFromTheCache(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	next := dockerConfig()
	revoked := revokedTestDigests(2)
	next["revoked_agent_pack_digests"] = revoked
	raw := encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))
	script := &conditionalScript{responses: []conditionalResponse{{raw: raw}}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if !reflect.DeepEqual(reload.Merged.Config.RevokedAgentPackDigests, []string{revoked[0].(string), revoked[1].(string)}) {
		t.Fatalf("revoked digests = %v", reload.Merged.Config.RevokedAgentPackDigests)
	}
	if err := reloader.Commit(reload); err != nil {
		t.Fatal(err)
	}
	var gateways []string
	restarted, err := Resolve(context.Background(), local.StateDir, testRunnerID,
		fetchReturning(nil, &NetworkError{Err: errors.New("connection refused")}, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.FromCache || !reflect.DeepEqual(restarted.Merged.Config.RevokedAgentPackDigests, reload.Merged.Config.RevokedAgentPackDigests) {
		t.Fatalf("restart from cache revoked digests = %v from cache = %v", restarted.Merged.Config.RevokedAgentPackDigests, restarted.FromCache)
	}
}
