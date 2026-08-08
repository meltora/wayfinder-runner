// our-runner-updater is the root-owned, capability-poor update service.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/example/app-runner/internal/updater"
	"gopkg.in/yaml.v3"
)

var version = "0.1.0"
var getEUID = os.Geteuid
var serveCommand = serve
var loadConfigCommand = loadConfig
var lookupUserCommand = user.Lookup
var loadTrustCommand = updater.LoadTrustFile
var newCoordinatorCommand = updater.New
var activationListenerCommand = activationListener
var runUpdaterServer = productionRunServer

type duration struct{ time.Duration }

func (d *duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a string")
	}
	parsed, err := time.ParseDuration(n.Value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("invalid duration %q", n.Value)
	}
	d.Duration = parsed
	return nil
}

type fileConfig struct {
	Enabled            bool     `yaml:"enabled"`
	ManifestURL        string   `yaml:"manifest_url"`
	Channel            string   `yaml:"channel"`
	TrustFile          string   `yaml:"trust_file"`
	AllowedOrigins     []string `yaml:"allowed_origins"`
	ExpectedRunnerUser string   `yaml:"expected_runner_user"`
	PrepareTimeout     duration `yaml:"prepare_timeout"`
	PreflightTimeout   duration `yaml:"preflight_timeout"`
	HealthTimeout      duration `yaml:"health_timeout"`
	ServiceTimeout     duration `yaml:"service_timeout"`
	MaxArchiveBytes    int64    `yaml:"max_archive_bytes"`
	MaxFileBytes       int64    `yaml:"max_file_bytes"`
	RetainVersions     int      `yaml:"retain_versions"`
}

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("our-runner-updater", version)
		return 0
	}
	if len(args) != 3 || args[0] != "serve" || args[1] != "--config" || args[2] == "" {
		fmt.Fprintln(os.Stderr, "usage: our-runner-updater serve --config /etc/our-runner/updater.yaml")
		return 2
	}
	if getEUID() != 0 {
		fmt.Fprintln(os.Stderr, "error: updater must run as root")
		return 1
	}
	if err := serveCommand(args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func serve(configPath string) error {
	cfg, err := loadConfigCommand(configPath, true)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	runnerUser, err := lookupUserCommand(cfg.ExpectedRunnerUser)
	if err != nil {
		return fmt.Errorf("resolve runner user: %w", err)
	}
	uid, err := strconv.Atoi(runnerUser.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(runnerUser.Gid)
	if err != nil {
		return err
	}
	trust, err := loadTrustCommand(cfg.TrustFile, true)
	if err != nil {
		return fmt.Errorf("load update trust: %w", err)
	}
	clock := structClock{}
	catalog := &updater.VerifiedCatalog{ManifestURL: cfg.ManifestURL, AllowedOrigins: cfg.AllowedOrigins, Trust: trust, Clock: clock}
	artifacts := &updater.HTTPArtifactFetcher{AllowedOrigins: cfg.AllowedOrigins, MaxSize: cfg.MaxArchiveBytes}
	service := &updater.SystemdController{Timeout: cfg.ServiceTimeout.Duration}
	core, err := newCoordinatorCommand(updater.Config{Channel: cfg.Channel, RunnerVersion: version, UpdaterVersion: version, VersionsDir: updater.DefaultVersionsDir, StateDir: updater.DefaultStateDir, CurrentLink: updater.DefaultCurrentLink, PreviousLink: updater.DefaultPreviousLink, UpdaterCurrentLink: updater.DefaultUpdaterCurrentLink, RunnerService: updater.DefaultRunnerService, PrepareTimeout: cfg.PrepareTimeout.Duration, HealthTimeout: cfg.HealthTimeout.Duration, MaxArchiveBytes: cfg.MaxArchiveBytes, MaxFileBytes: cfg.MaxFileBytes, RetainVersions: cfg.RetainVersions, RequireRootOwnership: true}, updater.Dependencies{Catalog: catalog, Artifacts: artifacts, Service: service, Preflight: &updater.CommandPreflight{Timeout: cfg.PreflightTimeout.Duration, RunnerUID: uid, RunnerGID: gid}, Clock: clock})
	if err != nil {
		return err
	}
	ln, activated, err := activationListenerCommand(os.Getenv, os.Getpid(), func(fd uintptr, name string) *os.File { return os.NewFile(fd, name) })
	if err != nil {
		return err
	}
	if !activated {
		if err := os.MkdirAll(filepath.Dir(updater.DefaultSocketPath), 0o750); err != nil {
			return err
		}
		ln, err = updater.ListenUnix(updater.DefaultSocketPath, 0o660)
		if err != nil {
			return err
		}
		if err := os.Chown(updater.DefaultSocketPath, 0, gid); err != nil {
			return errors.Join(err, ln.Close())
		}
		defer os.Remove(updater.DefaultSocketPath)
	} else if ln.Addr().Network() != "unix" || ln.Addr().String() != updater.DefaultSocketPath {
		return errors.Join(fmt.Errorf("activated socket must be %s", updater.DefaultSocketPath), ln.Close())
	}
	defer func() { _ = ln.Close() }()
	return runUpdaterServer(core, ln, uid, service)
}
func productionRunServer(core *updater.Coordinator, ln net.Listener, uid int, service *updater.SystemdController) error {
	auth := &updater.RunnerAuthorizer{ExpectedUID: uid, MainPID: func(ctx context.Context) (int, error) { return service.MainPID(ctx, updater.DefaultRunnerService) }}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return (&updater.Server{Coordinator: core, Peers: updater.OSPeerProvider{}, Authorizer: auth, Report: func(event, message string) { slog.Error("updater operation failed", "event", event, "error", message) }}).Serve(ctx, ln)
}

type structClock struct{}

func (structClock) Now() time.Time { return time.Now() }

func loadConfig(path string, requireRoot bool) (fileConfig, error) {
	var cfg fileConfig
	if !filepath.IsAbs(path) {
		return cfg, fmt.Errorf("config path must be absolute")
	}
	// #nosec G703 -- this privileged CLI intentionally accepts an absolute
	// operator-selected config path, then validates type, mode, size, and root
	// ownership before reading it.
	info, err := os.Lstat(path)
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o137 != 0 || info.Size() <= 0 || info.Size() > 256<<10 {
		return cfg, fmt.Errorf("config must be a private bounded regular file")
	}
	if requireRoot && !rootOwned(info) {
		return cfg, fmt.Errorf("config must be owned by root")
	}
	// #nosec G304,G703 -- the absolute path was lstat-checked as a private,
	// root-owned, bounded regular file immediately above.
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return cfg, fmt.Errorf("config has trailing YAML documents")
	}
	if err := validateConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
func validateConfig(c fileConfig) error {
	if !c.Enabled {
		return nil
	}
	if c.ManifestURL == "" || c.Channel == "" || c.TrustFile != "/etc/our-runner/update-trust.json" || c.ExpectedRunnerUser != "our-runner" || len(c.AllowedOrigins) == 0 {
		return fmt.Errorf("enabled updater requires manifest_url, channel, fixed trust_file, allowed_origins, and expected_runner_user")
	}
	if u, err := url.Parse(c.ManifestURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("manifest_url must use HTTPS")
	}
	for _, origin := range c.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("allowed_origins must contain HTTPS origins")
		}
	}
	if c.PrepareTimeout.Duration <= 0 || c.PreflightTimeout.Duration <= 0 || c.HealthTimeout.Duration <= 0 || c.ServiceTimeout.Duration <= 0 {
		return fmt.Errorf("all updater timeouts are required")
	}
	if c.MaxArchiveBytes <= 0 || c.MaxArchiveBytes > 1<<30 || c.MaxFileBytes <= 0 || c.MaxFileBytes > c.MaxArchiveBytes || c.RetainVersions < 2 || c.RetainVersions > 20 {
		return fmt.Errorf("invalid updater size or retention policy")
	}
	return nil
}
func rootOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

func activationListener(getenv func(string) string, pid int, fileAt func(uintptr, string) *os.File) (net.Listener, bool, error) {
	pidText, fdsText := getenv("LISTEN_PID"), getenv("LISTEN_FDS")
	if pidText == "" && fdsText == "" {
		return nil, false, nil
	}
	if pidText == "" || fdsText == "" {
		return nil, false, fmt.Errorf("incomplete systemd socket activation environment")
	}
	listenPID, err := strconv.Atoi(pidText)
	if err != nil || listenPID != pid {
		return nil, false, fmt.Errorf("invalid LISTEN_PID")
	}
	fds, err := strconv.Atoi(fdsText)
	if err != nil || fds != 1 {
		return nil, false, fmt.Errorf("exactly one activated socket is required")
	}
	file := fileAt(3, "our-runner-updater.socket")
	if file == nil {
		return nil, false, fmt.Errorf("activated socket fd 3 is unavailable")
	}
	defer file.Close()
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, false, fmt.Errorf("activated fd is not a listener: %w", err)
	}
	if _, ok := listener.(*net.UnixListener); !ok {
		return nil, false, errors.Join(fmt.Errorf("activated listener is not Unix"), listener.Close())
	}
	return listener, true, nil
}
