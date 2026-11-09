package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

type Status int

const (
	OK Status = iota
	Warn
	Fail
	Skip
)

type Check struct {
	Name   string
	Status Status
	Detail string
}

type Report struct {
	Checks []Check
}

func (r Report) ExitCode() int {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return 1
		}
	}
	return 0
}

type Options struct {
	ConfigurationLoaded        bool
	Provider                   string
	ProviderBinary             string
	RepositoryCount            int
	TrustedAgentPackKeyCount   int
	ControlPlaneURL            string
	StateDir                   string
	Isolation                  string
	JobImage                   string
	CheckImages                map[string]string
	JobNetwork                 string
	JobNetworkName             string
	EgressPolicyID             string
	WorkIntakeProposalEnabled  bool
	WorkIntakeProposalAdapter  string
	WorkIntakeProposalBinary   string
	EgressPolicyVerifierSocket string
	ProviderEnvironmentNames   map[string][]string
	PlatformTrustedKeySet      *v1.AgentPackTrustedKeySet
	BuildChannel               string
}

func runSystemChecks(ctx context.Context, opts Options) Report {
	var r Report
	r.Checks = append(r.Checks, checkControlPlaneDNS(opts.ControlPlaneURL))
	r.Checks = append(r.Checks, checkControlPlaneTCP(ctx, opts.ControlPlaneURL))
	r.Checks = append(r.Checks, checkCert(opts.StateDir))
	r.Checks = append(r.Checks, checkBinary("git"))
	r.Checks = append(r.Checks, checkBinary("docker"))
	r.Checks = append(r.Checks, checkDockerMode(ctx))
	mappingCheck, mapping, mappingErr := checkDockerUserMapping(ctx, opts)
	r.Checks = append(r.Checks, mappingCheck)
	r.Checks = append(r.Checks, checkRestrictedNetwork(ctx, opts))
	r.Checks = append(r.Checks, checkNestedSandbox(ctx, opts, mapping, mappingErr))
	r.Checks = append(r.Checks, checkProviderCLI(ctx, opts, mapping, mappingErr))
	r.Checks = append(r.Checks, checkCheckImages(ctx, opts))
	r.Checks = append(r.Checks, checkDisk(opts.StateDir))
	r.Checks = append(r.Checks, checkClock())
	return r
}

var probeNestedSandbox = isolation.ProbeNestedSandbox
var attestDockerNetwork = isolation.AttestDockerNetwork
var attestEffectivePolicy = func(
	ctx context.Context,
	socketPath string,
	attestation isolation.DockerNetworkAttestation,
) error {
	_, err := egresspolicy.NewClient(socketPath).Attest(ctx, attestation.ID, attestation.Name, attestation.PolicyID)
	return err
}

func checkRestrictedNetwork(ctx context.Context, opts Options) Check {
	c := Check{Name: "restricted-egress"}
	if opts.JobNetwork == "" {
		c.Status, c.Detail = Warn, "configuration not loaded; run doctor --config FILE for the production network gate"
		return c
	}
	if opts.JobNetwork == "none" {
		c.Status, c.Detail = OK, "job network is disabled"
		return c
	}
	if opts.JobNetwork != "restricted" {
		c.Status, c.Detail = Fail, "unknown job network mode"
		return c
	}
	attestation, err := attestDockerNetwork(ctx, opts.JobNetworkName, opts.EgressPolicyID)
	if err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	if opts.WorkIntakeProposalEnabled {
		if err := attestEffectivePolicy(ctx, opts.EgressPolicyVerifierSocket, attestation); err != nil {
			c.Status, c.Detail = Fail, "effective consultation egress policy: "+err.Error()
			return c
		}
		c.Status, c.Detail = OK, "verified effective leased egress policy "+opts.EgressPolicyID
		return c
	}
	c.Status, c.Detail = Warn, "network "+opts.JobNetworkName+" carries the labels for egress policy "+opts.EgressPolicyID+"; labels are not proof that egress is enforced on this host"
	return c
}

var resolveUserMapping = isolation.ResolveUserMapping

func checkDockerUserMapping(ctx context.Context, opts Options) (Check, isolation.UserMapping, error) {
	c := Check{Name: "docker-user-mapping"}
	if opts.Isolation == "" || opts.JobImage == "" {
		c.Status, c.Detail = Warn, "job user mapping not tested; run doctor --config FILE before production use"
		return c, isolation.UserMapping{}, nil
	}
	if opts.Isolation != "docker" {
		c.Status, c.Detail = Warn, "job user mapping not tested; direct isolation is development-only"
		return c, isolation.UserMapping{}, nil
	}
	mapping, err := resolveUserMapping(ctx, opts.JobImage, "doctor")
	if err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c, isolation.UserMapping{}, err
	}
	c.Status, c.Detail = OK, mapping.Describe()
	if mapping.Mode == isolation.UserMappingIdentity && runtime.GOOS == "linux" {
		c.Status, c.Detail = Warn, mapping.Describe()+"; daemon start refuses a rootful Linux daemon"
	}
	return c, mapping, nil
}

func checkNestedSandbox(ctx context.Context, opts Options, mapping isolation.UserMapping, mappingErr error) Check {
	c := Check{Name: "job-image-sandbox"}
	if opts.Isolation == "" || opts.JobImage == "" {
		c.Status, c.Detail = Warn, "job image not tested; run doctor --config FILE before production use"
		return c
	}
	if opts.Isolation != "docker" {
		c.Status, c.Detail = Warn, "direct isolation is development-only"
		return c
	}
	if mappingErr != nil {
		c.Status, c.Detail = Fail, "job user mapping: "+mappingErr.Error()
		return c
	}
	if err := probeNestedSandbox(ctx, opts.StateDir, opts.JobImage, mapping); err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	c.Status, c.Detail = OK, "nested sandbox started and denied a synthetic credential read"
	return c
}

var probeDocker = isolation.ProbeDocker

func checkDockerMode(ctx context.Context) Check {
	c := Check{Name: "docker-mode"}
	info := probeDocker(ctx)
	if !info.Available {
		if runtime.GOOS == "darwin" {
			c.Status, c.Detail = Warn, "docker not available; local development jobs cannot run"
		} else {
			c.Status, c.Detail = Fail, "docker not available; production jobs cannot run"
		}
		return c
	}
	if info.Rootless {
		c.Status, c.Detail = OK, "rootless docker "+info.ServerVersion
		return c
	}
	if runtime.GOOS == "darwin" {
		c.Status, c.Detail = Warn, "Docker Desktop VM "+info.ServerVersion+" is supported for development, not the Linux production gate"
		return c
	}
	if info.UsernsRemap {
		c.Status, c.Detail = Fail, "docker "+info.ServerVersion+" uses userns-remap; userns-remap is not supported yet, configure rootless Docker owned by the runner's user"
		return c
	}
	c.Status, c.Detail = Fail, "docker "+info.ServerVersion+" is rootful; configure rootless Docker owned by the runner's user"
	return c
}

func checkControlPlaneDNS(rawURL string) Check {
	c := Check{Name: "control-plane-dns"}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		c.Status, c.Detail = Fail, "invalid control_plane_url"
		return c
	}
	if _, err := net.LookupHost(u.Hostname()); err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	c.Status, c.Detail = OK, u.Hostname()
	return c
}

func checkControlPlaneTCP(ctx context.Context, rawURL string) Check {
	c := Check{Name: "control-plane-tcp"}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		c.Status, c.Detail = Fail, "invalid control_plane_url"
		return c
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	_ = conn.Close()
	c.Status, c.Detail = OK, net.JoinHostPort(u.Hostname(), port)
	return c
}

func checkCert(stateDir string) Check {
	c := Check{Name: "runner-certificate"}
	store := identity.NewStore(stateDir)
	_, _, err := store.LoadCert()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.Status, c.Detail = Warn, "not enrolled"
		} else {
			c.Status, c.Detail = Fail, "stored identity is unreadable or malformed: "+err.Error()
		}
		return c
	}
	rawID, err := safeio.ReadRegularFile(filepath.Join(stateDir, "runner-id"), 4096, 0o077)
	if err != nil {
		c.Status, c.Detail = Fail, "runner ID is unreadable or missing"
		return c
	}
	runnerID := strings.TrimSpace(string(rawID))
	if err := validation.ID("runner_id", runnerID); err != nil {
		c.Status, c.Detail = Fail, "runner ID is invalid"
		return c
	}
	if err := store.ValidateIdentity(runnerID, time.Now()); err != nil {
		c.Status, c.Detail = Fail, "stored identity verification failed: "+err.Error()
		return c
	}
	notBefore, notAfter, err := store.CertExpiry()
	if err != nil {
		c.Status, c.Detail = Fail, "unparseable certificate"
		return c
	}
	now := time.Now()
	if now.Before(notBefore) || !now.Before(notAfter) {
		c.Status, c.Detail = Fail, "certificate is not currently valid"
		return c
	}
	c.Status, c.Detail = OK, "enrolled; certificate expires "+notAfter.UTC().Format(time.RFC3339)
	return c
}

func checkBinary(name string) Check {
	c := Check{Name: "binary-" + name}
	if _, err := exec.LookPath(name); err != nil {
		c.Status, c.Detail = Warn, name+" not found"
		return c
	}
	c.Status, c.Detail = OK, name+" available"
	return c
}

func checkDisk(stateDir string) Check {
	c := Check{Name: "disk-space"}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	c.Status, c.Detail = OK, stateDir+" writable"
	return c
}

func checkClock() Check {
	return Check{Name: "clock", Status: OK, Detail: time.Now().UTC().Format(time.RFC3339)}
}

func Format(r Report) string {
	out := ""
	for _, c := range r.Checks {
		status := "OK  "
		switch c.Status {
		case Warn:
			status = "WARN"
		case Fail:
			status = "FAIL"
		case Skip:
			status = "SKIP"
		}
		out += fmt.Sprintf("[%s] %s: %s\n", status, c.Name, c.Detail)
	}
	return out
}
