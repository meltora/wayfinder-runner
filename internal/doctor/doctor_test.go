package doctor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/mockcp"
)

func withProbe(t *testing.T, info isolation.DockerInfo) {
	t.Helper()
	orig := probeDocker
	probeDocker = func(ctx context.Context) isolation.DockerInfo { return info }
	t.Cleanup(func() { probeDocker = orig })
}

var testRootlessMapping = isolation.UserMapping{
	Mode: isolation.UserMappingRootless, ContainerUID: 10001, ContainerGID: 10001, HostUID: 110000, HostGID: 110000,
}

func withUserMapping(t *testing.T, mapping isolation.UserMapping, err error) {
	t.Helper()
	original := resolveUserMapping
	resolveUserMapping = func(context.Context, string, string) (isolation.UserMapping, error) { return mapping, err }
	t.Cleanup(func() { resolveUserMapping = original })
}

func TestNestedSandboxRequiresLoadedConfiguration(t *testing.T) {
	c := checkNestedSandbox(context.Background(), Options{}, isolation.UserMapping{}, nil)
	if c.Status != Warn {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestNestedSandboxProbePassesAndFailsClosed(t *testing.T) {
	original := probeNestedSandbox
	t.Cleanup(func() { probeNestedSandbox = original })
	var probed isolation.UserMapping
	probeNestedSandbox = func(_ context.Context, _, _ string, mapping isolation.UserMapping) error {
		probed = mapping
		return nil
	}
	opts := Options{Isolation: "docker", JobImage: "registry/job@sha256:test", StateDir: t.TempDir()}
	if c := checkNestedSandbox(context.Background(), opts, testRootlessMapping, nil); c.Status != OK || probed != testRootlessMapping {
		t.Fatalf("status = %v: %s (probed with %+v)", c.Status, c.Detail, probed)
	}
	probeNestedSandbox = func(context.Context, string, string, isolation.UserMapping) error {
		return errors.New("bwrap unavailable")
	}
	if c := checkNestedSandbox(context.Background(), opts, testRootlessMapping, nil); c.Status != Fail {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestNestedSandboxFailsWithTheMappingReasonBeforeProbing(t *testing.T) {
	original := probeNestedSandbox
	t.Cleanup(func() { probeNestedSandbox = original })
	probeNestedSandbox = func(context.Context, string, string, isolation.UserMapping) error {
		t.Fatal("probe ran with an unsupported mapping")
		return nil
	}
	opts := Options{Isolation: "docker", JobImage: "registry/job@sha256:test", StateDir: t.TempDir()}
	reason := errors.New("the runner's host uid 1000 is not mapped into the Docker daemon's user namespace; userns-remap daemons are not supported yet")
	c := checkNestedSandbox(context.Background(), opts, isolation.UserMapping{}, reason)
	if c.Status != Fail || !strings.Contains(c.Detail, reason.Error()) {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestDockerUserMappingRow(t *testing.T) {
	opts := Options{Isolation: "docker", JobImage: "registry/job@sha256:test"}
	withUserMapping(t, testRootlessMapping, nil)
	c, mapping, err := checkDockerUserMapping(context.Background(), opts)
	if c.Name != "docker-user-mapping" || c.Status != OK || err != nil || mapping != testRootlessMapping ||
		c.Detail != "rootless: job user 10001:10001 (host 110000:110000); job output is re-owned after each container" {
		t.Fatalf("rootless row = %+v err=%v", c, err)
	}
	identity := isolation.UserMapping{Mode: isolation.UserMappingIdentity, ContainerUID: 501, ContainerGID: 20, HostUID: 501, HostGID: 20}
	withUserMapping(t, identity, nil)
	c, _, _ = checkDockerUserMapping(context.Background(), opts)
	want := Warn
	if runtime.GOOS == "darwin" {
		want = OK
	}
	if c.Status != want || !strings.Contains(c.Detail, "501:20") {
		t.Fatalf("identity row = %+v", c)
	}
	withUserMapping(t, isolation.UserMapping{}, isolation.ErrHostRoot)
	c, _, err = checkDockerUserMapping(context.Background(), opts)
	if c.Status != Fail || !errors.Is(err, isolation.ErrHostRoot) || !strings.Contains(c.Detail, "docker isolation must not run as host root") {
		t.Fatalf("host root row = %+v err=%v", c, err)
	}
	for _, unconfigured := range []Options{{}, {Isolation: "direct", JobImage: "registry/job@sha256:test"}} {
		c, _, _ = checkDockerUserMapping(context.Background(), unconfigured)
		if c.Status != Warn || !strings.Contains(c.Detail, "not tested") {
			t.Fatalf("unconfigured row = %+v", c)
		}
	}
}

func TestRestrictedNetworkVerification(t *testing.T) {
	originalNetwork := attestDockerNetwork
	originalPolicy := attestEffectivePolicy
	t.Cleanup(func() {
		attestDockerNetwork = originalNetwork
		attestEffectivePolicy = originalPolicy
	})
	called := false
	attestation := isolation.DockerNetworkAttestation{
		ID: strings.Repeat("a", 64), Name: "egress", PolicyID: "policy-v1", Driver: "bridge", Scope: "local",
	}
	attestDockerNetwork = func(context.Context, string, string) (isolation.DockerNetworkAttestation, error) {
		called = true
		return attestation, nil
	}
	opts := Options{JobNetwork: "restricted", JobNetworkName: "egress", EgressPolicyID: "policy-v1"}
	labelOnly := checkRestrictedNetwork(context.Background(), opts)
	if labelOnly.Status != Warn || !called ||
		labelOnly.Detail != "network egress carries the labels for egress policy policy-v1; labels are not proof that egress is enforced on this host" {
		t.Fatalf("status = %v, called = %t: %s", labelOnly.Status, called, labelOnly.Detail)
	}
	if code := (Report{Checks: []Check{labelOnly}}).ExitCode(); code != 0 {
		t.Fatalf("label-only exit code = %d", code)
	}
	if c := checkRestrictedNetwork(context.Background(), Options{JobNetwork: "none"}); c.Status != OK || c.Detail != "job network is disabled" {
		t.Fatalf("none status = %v: %s", c.Status, c.Detail)
	}
	policyCalled := false
	attestEffectivePolicy = func(context.Context, string, isolation.DockerNetworkAttestation) error {
		policyCalled = true
		return nil
	}
	opts.WorkIntakeProposalEnabled = true
	opts.EgressPolicyVerifierSocket = "/run/our-runner-egress/policy.sock"
	if c := checkRestrictedNetwork(context.Background(), opts); c.Status != OK || !policyCalled {
		t.Fatalf("effective status = %v, called = %t: %s", c.Status, policyCalled, c.Detail)
	}
	attestEffectivePolicy = func(context.Context, string, isolation.DockerNetworkAttestation) error {
		return errors.New("policy lease unavailable")
	}
	if c := checkRestrictedNetwork(context.Background(), opts); c.Status != Fail {
		t.Fatalf("missing effective policy status = %v: %s", c.Status, c.Detail)
	}
}

func TestRestrictedNetworkAttestationFailureFails(t *testing.T) {
	originalNetwork := attestDockerNetwork
	t.Cleanup(func() { attestDockerNetwork = originalNetwork })
	for _, failure := range []string{
		"docker network our-runner-jobs-dev was not found",
		"docker network egress-policy label does not match local-dev-egress-not-enforced",
		"docker network must use the bridge driver",
	} {
		attestDockerNetwork = func(context.Context, string, string) (isolation.DockerNetworkAttestation, error) {
			return isolation.DockerNetworkAttestation{}, errors.New(failure)
		}
		c := checkRestrictedNetwork(context.Background(), Options{JobNetwork: "restricted", JobNetworkName: "our-runner-jobs-dev", EgressPolicyID: "local-dev-egress-not-enforced"})
		if c.Status != Fail || c.Detail != failure {
			t.Fatalf("status = %v: %s", c.Status, c.Detail)
		}
		if code := (Report{Checks: []Check{c}}).ExitCode(); code != 1 {
			t.Fatalf("exit code = %d", code)
		}
	}
}

func TestRestrictedNetworkLabelOnlyNamesTheLocalDevelopmentPolicyVerbatim(t *testing.T) {
	originalNetwork := attestDockerNetwork
	t.Cleanup(func() { attestDockerNetwork = originalNetwork })
	attestDockerNetwork = func(context.Context, string, string) (isolation.DockerNetworkAttestation, error) {
		return isolation.DockerNetworkAttestation{Name: "our-runner-jobs-dev", PolicyID: "local-dev-egress-not-enforced"}, nil
	}
	c := checkRestrictedNetwork(context.Background(), Options{JobNetwork: "restricted", JobNetworkName: "our-runner-jobs-dev", EgressPolicyID: "local-dev-egress-not-enforced"})
	if c.Status != Warn || !strings.Contains(c.Detail, "network our-runner-jobs-dev") || !strings.Contains(c.Detail, "egress policy local-dev-egress-not-enforced") {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestDockerModeRootless(t *testing.T) {
	withProbe(t, isolation.DockerInfo{Available: true, Rootless: true, ServerVersion: "26.1.0"})
	c := checkDockerMode(context.Background())
	if c.Status != OK {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestDockerModeUsernsRemapFailsClosed(t *testing.T) {
	withProbe(t, isolation.DockerInfo{Available: true, UsernsRemap: true, ServerVersion: "26.1.0"})
	c := checkDockerMode(context.Background())
	if c.Status != Fail || !strings.Contains(c.Detail, "userns-remap is not supported yet") {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestDockerModeRootfulWarns(t *testing.T) {
	withProbe(t, isolation.DockerInfo{Available: true, ServerVersion: "26.1.0"})
	c := checkDockerMode(context.Background())
	want := Fail
	if runtime.GOOS == "darwin" {
		want = Warn
	}
	if c.Status != want {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestDockerModeUnavailable(t *testing.T) {
	withProbe(t, isolation.DockerInfo{})
	c := checkDockerMode(context.Background())
	want := Fail
	if runtime.GOOS == "darwin" {
		want = Warn
	}
	if c.Status != want {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
}

func TestCertificateCheckVerifiesRunnerAndChain(t *testing.T) {
	stateDir := t.TempDir()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.NewCSR(key, "runner-7")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, "runner-7")
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.NewStore(stateDir).SaveIdentity(key, cert, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runner-id"), []byte("runner-7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkCert(stateDir); c.Status != OK {
		t.Fatalf("status = %v: %s", c.Status, c.Detail)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runner-id"), []byte("runner-8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkCert(stateDir); c.Status != Fail {
		t.Fatalf("wrong runner status = %v: %s", c.Status, c.Detail)
	}
}

func TestCertificateCheckDistinguishesMissingAndCorruptIdentity(t *testing.T) {
	stateDir := t.TempDir()
	if c := checkCert(stateDir); c.Status != Warn {
		t.Fatalf("missing status = %v: %s", c.Status, c.Detail)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "identity.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkCert(stateDir); c.Status != Fail {
		t.Fatalf("corrupt status = %v: %s", c.Status, c.Detail)
	}
}

func TestDoctorFormattingAndExitCode(t *testing.T) {
	r := Report{Checks: []Check{{Name: "a", Status: OK, Detail: "yes"}, {Name: "b", Status: Warn, Detail: "maybe"}}}
	if r.ExitCode() != 0 || !strings.Contains(Format(r), "[WARN] b: maybe") {
		t.Fatalf("report = %q, exit = %d", Format(r), r.ExitCode())
	}
	r.Checks = append(r.Checks, Check{Name: "c", Status: Fail, Detail: "no"})
	if r.ExitCode() != 1 || !strings.Contains(Format(r), "[FAIL] c: no") {
		t.Fatalf("report = %q, exit = %d", Format(r), r.ExitCode())
	}
	if checkClock().Status != OK || checkDisk(t.TempDir()).Status != OK || checkBinary("go").Status != OK {
		t.Fatal("basic doctor checks failed")
	}
	if c := checkBinary("definitely-not-a-real-runner-binary"); c.Status != Warn {
		t.Fatalf("missing binary status = %v", c.Status)
	}
}

func TestRunExecutesCompleteConfiguredPreflight(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	originalDocker := probeDocker
	originalSandbox := probeNestedSandbox
	originalNetwork := attestDockerNetwork
	originalPolicy := attestEffectivePolicy
	t.Cleanup(func() {
		probeDocker = originalDocker
		probeNestedSandbox = originalSandbox
		attestDockerNetwork = originalNetwork
		attestEffectivePolicy = originalPolicy
	})
	probeDocker = func(context.Context) isolation.DockerInfo {
		return isolation.DockerInfo{Available: true, Rootless: true, ServerVersion: "test"}
	}
	probeNestedSandbox = func(context.Context, string, string, isolation.UserMapping) error { return nil }
	attestDockerNetwork = func(context.Context, string, string) (isolation.DockerNetworkAttestation, error) {
		return isolation.DockerNetworkAttestation{}, nil
	}
	withUserMapping(t, testRootlessMapping, nil)

	report := Run(context.Background(), Options{
		ControlPlaneURL: "wss://" + listener.Addr().String() + "/stream",
		StateDir:        t.TempDir(),
		Isolation:       "docker",
		JobImage:        "registry/job@sha256:test",
		JobNetwork:      "none",
	})
	if report.ExitCode() != 0 {
		t.Fatalf("preflight failed:\n%s", Format(report))
	}
	if len(report.Checks) != 11 {
		t.Fatalf("checks = %d, want 11", len(report.Checks))
	}
	names := make([]string, len(report.Checks))
	for index, check := range report.Checks {
		names[index] = check.Name
	}
	if joined := strings.Join(names, " "); !strings.Contains(joined, "docker-mode docker-user-mapping restricted-egress job-image-sandbox") {
		t.Fatalf("check order = %s", joined)
	}
}

func TestControlPlaneAddressChecksFailClosed(t *testing.T) {
	for _, raw := range []string{"", "://bad", "wss:///missing-host"} {
		if c := checkControlPlaneDNS(raw); c.Status != Fail {
			t.Fatalf("DNS %q status = %v", raw, c.Status)
		}
		if c := checkControlPlaneTCP(context.Background(), raw); c.Status != Fail {
			t.Fatalf("TCP %q status = %v", raw, c.Status)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()
	if c := checkControlPlaneTCP(context.Background(), fmt.Sprintf("wss://127.0.0.1:%d", address.Port)); c.Status != Fail {
		t.Fatalf("closed port status = %v: %s", c.Status, c.Detail)
	}
}
