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
	ControlPlaneURL string
	StateDir        string
	Isolation       string
	JobImage        string
	JobNetwork      string
	JobNetworkName  string
	EgressPolicyID  string
}

func Run(ctx context.Context, opts Options) Report {
	var r Report
	r.Checks = append(r.Checks, checkControlPlaneDNS(opts.ControlPlaneURL))
	r.Checks = append(r.Checks, checkControlPlaneTCP(ctx, opts.ControlPlaneURL))
	r.Checks = append(r.Checks, checkCert(opts.StateDir))
	r.Checks = append(r.Checks, checkBinary("git"))
	r.Checks = append(r.Checks, checkBinary("docker"))
	r.Checks = append(r.Checks, checkDockerMode(ctx))
	r.Checks = append(r.Checks, checkRestrictedNetwork(ctx, opts))
	r.Checks = append(r.Checks, checkNestedSandbox(ctx, opts))
	r.Checks = append(r.Checks, checkDisk(opts.StateDir))
	r.Checks = append(r.Checks, checkClock())
	return r
}

var probeNestedSandbox = isolation.ProbeNestedSandbox
var verifyDockerNetwork = isolation.VerifyDockerNetwork

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
	if err := verifyDockerNetwork(ctx, opts.JobNetworkName, opts.EgressPolicyID); err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c
	}
	c.Status, c.Detail = OK, "verified externally enforced network policy "+opts.EgressPolicyID
	return c
}

func checkNestedSandbox(ctx context.Context, opts Options) Check {
	c := Check{Name: "job-image-sandbox"}
	if opts.Isolation == "" || opts.JobImage == "" {
		c.Status, c.Detail = Warn, "job image not tested; run doctor --config FILE before production use"
		return c
	}
	if opts.Isolation != "docker" {
		c.Status, c.Detail = Warn, "direct isolation is development-only"
		return c
	}
	if err := probeNestedSandbox(ctx, opts.StateDir, opts.JobImage); err != nil {
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
	if info.UsernsRemap {
		c.Status, c.Detail = OK, "docker "+info.ServerVersion+" with userns remap"
		return c
	}
	if runtime.GOOS == "darwin" {
		c.Status, c.Detail = Warn, "Docker Desktop VM "+info.ServerVersion+" is supported for development, not the Linux production gate"
		return c
	}
	c.Status, c.Detail = Fail, "docker "+info.ServerVersion+" is rootful; configure rootless Docker or userns-remap"
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
		}
		out += fmt.Sprintf("[%s] %s: %s\n", status, c.Name, c.Detail)
	}
	return out
}
