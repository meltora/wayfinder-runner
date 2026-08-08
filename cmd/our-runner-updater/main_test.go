package main

import (
	"context"
	"errors"
	"github.com/example/app-runner/internal/updatecatalog"
	"github.com/example/app-runner/internal/updater"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

func TestDisabledConfigIsValidAndVersionCommandSucceeds(t *testing.T) {
	t.Parallel()
	if err := validateConfig(fileConfig{}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"version"}); code != 0 {
		t.Fatalf("version exit=%d", code)
	}
}

func TestLoadEnabledConfigStrictly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "updater.yaml")
	raw := []byte(`enabled: true
manifest_url: https://releases.example.test/stable.json
channel: stable
trust_file: /etc/our-runner/update-trust.json
allowed_origins:
  - https://releases.example.test
expected_runner_user: our-runner
prepare_timeout: 15m
preflight_timeout: 30s
health_timeout: 90s
service_timeout: 30s
max_archive_bytes: 268435456
max_file_bytes: 134217728
retain_versions: 3
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Channel != "stable" {
		t.Fatalf("config=%+v", cfg)
	}
	if err := os.WriteFile(path, append(raw, []byte("unknown_field: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path, false); err == nil {
		t.Fatal("unknown config field accepted")
	}
}

func TestEnabledConfigFailsClosedWithoutTrustOrOrigin(t *testing.T) {
	t.Parallel()
	cfg := fileConfig{Enabled: true, ManifestURL: "https://example.test/x", Channel: "stable", ExpectedRunnerUser: "our-runner"}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("incomplete enabled config accepted")
	}
}

func TestRunDispatchAndErrors(t *testing.T) {
	oldEUID, oldServe := getEUID, serveCommand
	t.Cleanup(func() { getEUID = oldEUID; serveCommand = oldServe })
	if code := run(nil); code != 2 {
		t.Fatalf("empty=%d", code)
	}
	if code := run([]string{"serve"}); code != 2 {
		t.Fatalf("bad args=%d", code)
	}
	getEUID = func() int { return 1000 }
	if code := run([]string{"serve", "--config", "/x"}); code != 1 {
		t.Fatalf("nonroot=%d", code)
	}
	getEUID = func() int { return 0 }
	serveCommand = func(string) error { return nil }
	if code := run([]string{"serve", "--config", "/x"}); code != 0 {
		t.Fatalf("success=%d", code)
	}
	serveCommand = func(string) error { return errors.New("boom") }
	if code := run([]string{"serve", "--config", "/x"}); code != 1 {
		t.Fatalf("failure=%d", code)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "unix" }
func (a fakeAddr) String() string  { return string(a) }

type fakeListener struct{ closed bool }

func (f *fakeListener) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (f *fakeListener) Close() error              { f.closed = true; return nil }
func (f *fakeListener) Addr() net.Addr            { return fakeAddr(updater.DefaultSocketPath) }

func TestServeWiresEnabledRootConfiguration(t *testing.T) {
	oldLoad, oldLookup, oldTrust, oldNew, oldActivation, oldRun := loadConfigCommand, lookupUserCommand, loadTrustCommand, newCoordinatorCommand, activationListenerCommand, runUpdaterServer
	t.Cleanup(func() {
		loadConfigCommand = oldLoad
		lookupUserCommand = oldLookup
		loadTrustCommand = oldTrust
		newCoordinatorCommand = oldNew
		activationListenerCommand = oldActivation
		runUpdaterServer = oldRun
	})
	cfg := validEnabledConfig()
	loadConfigCommand = func(string, bool) (fileConfig, error) { return cfg, nil }
	lookupUserCommand = func(string) (*user.User, error) { return &user.User{Uid: "123", Gid: "456"}, nil }
	loadTrustCommand = func(string, bool) (updatecatalog.TrustPolicy, error) { return updatecatalog.TrustPolicy{}, nil }
	core := &updater.Coordinator{}
	newCoordinatorCommand = func(config updater.Config, deps updater.Dependencies) (*updater.Coordinator, error) {
		if config.UpdaterCurrentLink != updater.DefaultUpdaterCurrentLink {
			t.Fatalf("config=%+v", config)
		}
		preflight := deps.Preflight.(*updater.CommandPreflight)
		if preflight.RunnerUID != 123 || preflight.RunnerGID != 456 {
			t.Fatalf("preflight=%+v", preflight)
		}
		return core, nil
	}
	listener := &fakeListener{}
	activationListenerCommand = func(func(string) string, int, func(uintptr, string) *os.File) (net.Listener, bool, error) {
		return listener, true, nil
	}
	called := false
	runUpdaterServer = func(got *updater.Coordinator, ln net.Listener, uid int, _ *updater.SystemdController) error {
		called = true
		if got != core || ln != listener || uid != 123 {
			t.Fatal("runtime wiring mismatch")
		}
		return nil
	}
	if err := serve("/ignored"); err != nil {
		t.Fatal(err)
	}
	if !called || !listener.closed {
		t.Fatal("runtime or cleanup not invoked")
	}
}

func TestServeDependencyFailuresAndDisabled(t *testing.T) {
	oldLoad, oldLookup, oldTrust := loadConfigCommand, lookupUserCommand, loadTrustCommand
	t.Cleanup(func() { loadConfigCommand = oldLoad; lookupUserCommand = oldLookup; loadTrustCommand = oldTrust })
	loadConfigCommand = func(string, bool) (fileConfig, error) { return fileConfig{}, errors.New("config") }
	if err := serve("/x"); err == nil {
		t.Fatal("config error ignored")
	}
	loadConfigCommand = func(string, bool) (fileConfig, error) { return fileConfig{}, nil }
	if err := serve("/x"); err != nil {
		t.Fatal(err)
	}
	loadConfigCommand = func(string, bool) (fileConfig, error) { return validEnabledConfig(), nil }
	lookupUserCommand = func(string) (*user.User, error) { return nil, errors.New("lookup") }
	if err := serve("/x"); err == nil {
		t.Fatal("lookup error ignored")
	}
	lookupUserCommand = func(string) (*user.User, error) { return &user.User{Uid: "bad", Gid: "1"}, nil }
	if err := serve("/x"); err == nil {
		t.Fatal("bad uid accepted")
	}
	lookupUserCommand = func(string) (*user.User, error) { return &user.User{Uid: "1", Gid: "2"}, nil }
	loadTrustCommand = func(string, bool) (updatecatalog.TrustPolicy, error) {
		return updatecatalog.TrustPolicy{}, errors.New("trust")
	}
	if err := serve("/x"); err == nil {
		t.Fatal("trust error ignored")
	}
}

func TestServeCoordinatorAndListenerFailures(t *testing.T) {
	oldLoad, oldLookup, oldTrust, oldNew, oldActivation := loadConfigCommand, lookupUserCommand, loadTrustCommand, newCoordinatorCommand, activationListenerCommand
	t.Cleanup(func() {
		loadConfigCommand = oldLoad
		lookupUserCommand = oldLookup
		loadTrustCommand = oldTrust
		newCoordinatorCommand = oldNew
		activationListenerCommand = oldActivation
	})
	loadConfigCommand = func(string, bool) (fileConfig, error) { return validEnabledConfig(), nil }
	loadTrustCommand = func(string, bool) (updatecatalog.TrustPolicy, error) { return updatecatalog.TrustPolicy{}, nil }
	lookupUserCommand = func(string) (*user.User, error) { return &user.User{Uid: "1", Gid: "bad"}, nil }
	if err := serve("/x"); err == nil {
		t.Fatal("bad gid accepted")
	}
	lookupUserCommand = func(string) (*user.User, error) { return &user.User{Uid: "1", Gid: "2"}, nil }
	newCoordinatorCommand = func(updater.Config, updater.Dependencies) (*updater.Coordinator, error) {
		return nil, errors.New("core")
	}
	if err := serve("/x"); err == nil {
		t.Fatal("coordinator error ignored")
	}
	newCoordinatorCommand = func(updater.Config, updater.Dependencies) (*updater.Coordinator, error) {
		return &updater.Coordinator{}, nil
	}
	activationListenerCommand = func(func(string) string, int, func(uintptr, string) *os.File) (net.Listener, bool, error) {
		return nil, false, errors.New("socket")
	}
	if err := serve("/x"); err == nil {
		t.Fatal("listener error ignored")
	}
}

func validEnabledConfig() fileConfig {
	return fileConfig{Enabled: true, ManifestURL: "https://release.test/manifest", Channel: "stable", TrustFile: updater.DefaultTrustFile, AllowedOrigins: []string{"https://release.test"}, ExpectedRunnerUser: "our-runner", PrepareTimeout: duration{time.Minute}, PreflightTimeout: duration{time.Second}, HealthTimeout: duration{time.Minute}, ServiceTimeout: duration{time.Second}, MaxArchiveBytes: 1 << 20, MaxFileBytes: 1 << 19, RetainVersions: 3}
}

type cliCatalog struct{}

func (cliCatalog) Latest(context.Context, updater.CatalogQuery) (updater.Release, error) {
	return updater.Release{}, errors.New("unused")
}

type cliArtifact struct{}

func (cliArtifact) Download(context.Context, string, int64, string, io.Writer) error {
	return errors.New("unused")
}

type cliService struct{}

func (cliService) Stop(context.Context, string) error           { return nil }
func (cliService) Start(context.Context, string) error          { return nil }
func (cliService) MainPID(context.Context, string) (int, error) { return 1, nil }

type cliPreflight struct{}

func (cliPreflight) Check(context.Context, string) error { return nil }

func TestProductionRunServerAndClock(t *testing.T) {
	dir := t.TempDir()
	core, err := updater.New(updater.Config{Channel: "stable", VersionsDir: filepath.Join(dir, "versions"), StateDir: filepath.Join(dir, "state"), CurrentLink: filepath.Join(dir, "current"), PreviousLink: filepath.Join(dir, "previous"), UpdaterCurrentLink: filepath.Join(dir, "updater"), RunnerService: updater.DefaultRunnerService}, updater.Dependencies{Catalog: cliCatalog{}, Artifacts: cliArtifact{}, Service: cliService{}, Preflight: cliPreflight{}})
	if err != nil {
		t.Fatal(err)
	}
	listener := &fakeListener{}
	if err := productionRunServer(core, listener, 123, &updater.SystemdController{}); err == nil {
		t.Fatal("listener failure ignored")
	}
	if (structClock{}).Now().IsZero() {
		t.Fatal("clock returned zero")
	}
}

func TestConfigBoundaryFailures(t *testing.T) {
	t.Parallel()
	if _, err := loadConfig("relative", false); err == nil {
		t.Fatal("relative config accepted")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("enabled: false\n---\nenabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path, false); err == nil {
		t.Fatal("multiple documents accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path, false); err == nil {
		t.Fatal("public config accepted")
	}
	invalid := validEnabledConfig()
	invalid.AllowedOrigins = []string{"http://example.test"}
	if err := validateConfig(invalid); err == nil {
		t.Fatal("HTTP origin accepted")
	}
	invalid = validEnabledConfig()
	invalid.MaxArchiveBytes = 0
	if err := validateConfig(invalid); err == nil {
		t.Fatal("invalid sizes accepted")
	}
	var d duration
	if err := d.UnmarshalYAML(&yaml.Node{Kind: yaml.MappingNode}); err == nil {
		t.Fatal("mapping duration accepted")
	}
	if err := d.UnmarshalYAML(&yaml.Node{Kind: yaml.ScalarNode, Value: "bad"}); err == nil {
		t.Fatal("bad duration accepted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = rootOwned(info)
}
