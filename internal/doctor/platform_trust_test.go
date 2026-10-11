package doctor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/agentpack/trustset"
)

type doctorTrustSigner struct{ private ed25519.PrivateKey }

func (signer doctorTrustSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(signer.private, message), nil
}

func (signer doctorTrustSigner) PublicKeySPKI() ([]byte, error) {
	return x509.MarshalPKIXPublicKey(signer.private.Public())
}

func doctorTrustRoot(t *testing.T) (doctorTrustSigner, trustroot.Roots) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	root := trustroot.Root{KeyID: hex.EncodeToString(sum[:]), PublicKeySPKIBase64: base64.StdEncoding.EncodeToString(der), PublicKey: public}
	return doctorTrustSigner{private: private}, trustroot.Roots{Channel: trustroot.ChannelProduction, Source: trustroot.SourceEmbedded, Keys: []trustroot.Root{root}}
}

func doctorTrustSet(t *testing.T, signer doctorTrustSigner, sequence int64) v1.AgentPackTrustedKeySet {
	t.Helper()
	document := trustset.Document{Format: trustset.Format, Sequence: sequence, IssuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), RevokedKeyIDs: []string{}}
	signed, err := trustset.Sign(context.Background(), signer, document)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func platformTrustReport(t *testing.T, opts Options, roots func() (trustroot.Roots, error)) Check {
	t.Helper()
	deps := setupDependencies()
	deps.PlatformTrustRoots = roots
	opts.ConfigurationLoaded = true
	opts.Provider = "codex"
	report := RunWithDependencies(context.Background(), opts, deps)
	check := findCheck(t, report, "platform-trust")
	for _, legacy := range []string{"agent-pack-trust", "agent-pack-keys-awaiting"} {
		for _, c := range report.Checks {
			if c.Name == legacy {
				t.Fatalf("legacy check %s is still reported: %+v", legacy, c)
			}
		}
	}
	if regexp.MustCompile(`(?i)agent ?pack`).MatchString(check.Detail) {
		t.Fatalf("platform trust detail names the runtime package: %q", check.Detail)
	}
	return check
}

func TestPlatformTrustPassesWhenTheAppliedSetVerifies(t *testing.T) {
	signer, roots := doctorTrustRoot(t)
	stateDir := t.TempDir()
	store, err := trustset.OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(doctorTrustSet(t, signer, 4)); err != nil {
		t.Fatal(err)
	}
	check := platformTrustReport(t, Options{StateDir: stateDir}, func() (trustroot.Roots, error) { return roots, nil })
	if check.Status != OK || !strings.HasPrefix(check.Detail, "Platform trust current") || !strings.Contains(check.Detail, "sequence 4") {
		t.Fatalf("check = %+v", check)
	}
}

func TestPlatformTrustPassesForADeliveredSetNotYetApplied(t *testing.T) {
	signer, roots := doctorTrustRoot(t)
	set := doctorTrustSet(t, signer, 2)
	check := platformTrustReport(t, Options{StateDir: t.TempDir(), PlatformTrustedKeySet: &set}, func() (trustroot.Roots, error) { return roots, nil })
	if check.Status != OK || !strings.Contains(check.Detail, "sequence 2") {
		t.Fatalf("check = %+v", check)
	}
}

func TestPlatformTrustFailsAndAsksForAnUpgradeWhenTheSetDoesNotVerify(t *testing.T) {
	signer, roots := doctorTrustRoot(t)
	_, otherRoots := doctorTrustRoot(t)
	stateDir := t.TempDir()
	store, err := trustset.OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(doctorTrustSet(t, signer, 1)); err != nil {
		t.Fatal(err)
	}
	stored := platformTrustReport(t, Options{StateDir: stateDir}, func() (trustroot.Roots, error) { return otherRoots, nil })
	delivered := doctorTrustSet(t, signer, 1)
	configured := platformTrustReport(t, Options{StateDir: t.TempDir(), PlatformTrustedKeySet: &delivered}, func() (trustroot.Roots, error) { return otherRoots, nil })
	noRoots := platformTrustReport(t, Options{StateDir: t.TempDir()}, func() (trustroot.Roots, error) {
		return trustroot.Roots{Channel: trustroot.ChannelProduction, Source: trustroot.SourceEmbedded}, nil
	})
	for name, check := range map[string]Check{"stored": stored, "configured": configured, "no roots": noRoots} {
		if check.Status != Fail || !strings.HasPrefix(check.Detail, "Platform trust is not current") || !strings.Contains(check.Detail, "upgrade the runner") {
			t.Fatalf("%s: check = %+v", name, check)
		}
	}
}

func TestPlatformTrustReportsRefusedRootsAndCorruptState(t *testing.T) {
	_, roots := doctorTrustRoot(t)
	refused := platformTrustReport(t, Options{StateDir: t.TempDir()}, func() (trustroot.Roots, error) {
		return trustroot.Roots{}, errors.New("the development trust root override is refused on production builds")
	})
	if refused.Status != Fail || !strings.Contains(refused.Detail, "refused") {
		t.Fatalf("refused = %+v", refused)
	}
	pending := platformTrustReport(t, Options{StateDir: t.TempDir()}, func() (trustroot.Roots, error) { return roots, nil })
	if pending.Status != Warn || !strings.Contains(pending.Detail, "not yet delivered") {
		t.Fatalf("pending = %+v", pending)
	}
	unevaluated := platformTrustReport(t, Options{StateDir: t.TempDir()}, nil)
	if unevaluated.Status != Warn || !strings.Contains(unevaluated.Detail, "not evaluated") {
		t.Fatalf("unevaluated = %+v", unevaluated)
	}
}
