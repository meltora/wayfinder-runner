package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/agentpack/trustset"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/lease"
)

type trustTestSigner struct{ private ed25519.PrivateKey }

func (signer trustTestSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(signer.private, message), nil
}

func (signer trustTestSigner) PublicKeySPKI() ([]byte, error) {
	return x509.MarshalPKIXPublicKey(signer.private.Public())
}

type trustTestKey struct {
	private ed25519.PrivateKey
	spki    string
	keyID   string
}

func newTrustTestKey(t *testing.T) trustTestKey {
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
	return trustTestKey{private: private, spki: base64.StdEncoding.EncodeToString(der), keyID: hex.EncodeToString(sum[:])}
}

func (key trustTestKey) root() trustroot.Root {
	return trustroot.Root{KeyID: key.keyID, PublicKeySPKIBase64: key.spki, PublicKey: key.private.Public().(ed25519.PublicKey)}
}

func trustTestRoots(keys ...trustTestKey) trustroot.Roots {
	roots := trustroot.Roots{Channel: trustroot.ChannelDevelopment, Source: trustroot.SourceEmbedded}
	for _, key := range keys {
		roots.Keys = append(roots.Keys, key.root())
	}
	return roots
}

type trustTestSetKey struct {
	id        string
	key       trustTestKey
	notBefore time.Time
	notAfter  *time.Time
}

var trustTestEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func signTrustTestSet(t *testing.T, root trustTestKey, sequence int64, keys []trustTestSetKey, revoked ...string) v1.AgentPackTrustedKeySet {
	t.Helper()
	document := trustset.Document{Format: trustset.Format, Sequence: sequence, IssuedAt: trustTestEpoch, RevokedKeyIDs: append([]string{}, revoked...)}
	for _, key := range keys {
		notBefore := key.notBefore
		if notBefore.IsZero() {
			notBefore = trustTestEpoch
		}
		document.Keys = append(document.Keys, trustset.Key{KeyID: key.id, PublicKeySPKIBase64: key.key.spki, NotBefore: notBefore, NotAfter: key.notAfter})
	}
	normalized, err := trustset.ParseInput(document.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	signed, err := trustset.Sign(context.Background(), trustTestSigner{private: root.private}, normalized)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func packJobSignedBy(t *testing.T, keyID string, key trustTestKey, set *v1.AgentPackTrustedKeySet) v1.JobSpec {
	t.Helper()
	ref := v1.AgentPackRef{
		Version: "1.0.0", Digest: strings.Repeat("a", 64),
		URL: "https://packs.example.test/pack-1.0.0.tgz", KeyID: keyID,
	}
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "pack-version-1",
		Scope:   v1.AgentPackAuthorityScope{Kind: v1.AgentPackScopePlatform},
		PackKey: "default-pack", PackName: "Default Pack", Provider: "codex",
		ProtocolVersion: 1, MinimumRunnerVersion: "0.1.0", AgentPack: ref,
	}
	digest, err := agentpack.PublicationAttestationDigest(authority)
	if err != nil {
		t.Fatal(err)
	}
	authority.AttestationDigest = digest
	digestBytes, _ := hex.DecodeString(digest)
	authority.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(key.private, digestBytes))
	return withTestAuthority(v1.JobSpec{
		JobID: "job-pack", AttemptID: "attempt-pack", ProjectID: "project-1", Kind: v1.JobKindPlan,
		FencingToken: 1, Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:    v1.TicketContext{Key: "PROJ-1", Summary: "Use signed pack"},
		AgentPack: &ref, AgentPackAuthority: &authority, AgentPackTrustedKeySet: set,
	})
}

func trustTestDaemon(t *testing.T, roots trustroot.Roots, developmentKeysAllowed bool, developmentKeys map[string]string) *Daemon {
	t.Helper()
	stateDir := t.TempDir()
	store, err := trustset.OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{
		cfg: config.Normalize(config.Config{
			StateDir: stateDir, Provider: "codex", AgentPackPublicKeys: developmentKeys,
			AllowedArtifactOrigins: []string{"https://packs.example.test"},
		}),
		agentPackTrust: &agentPackTrust{store: store, developmentKeysAllowed: developmentKeysAllowed},
		leases:         lease.NewManager(),
		logger:         slog.New(slog.DiscardHandler),
		now:            func() time.Time { return trustTestEpoch.Add(24 * time.Hour) },
	}
}

func assertTrustRejection(t *testing.T, d *Daemon, job v1.JobSpec, want v1.CommandAckReasonCode) {
	t.Helper()
	err := d.startJob(context.Background(), job)
	if reason := commandRejectionReason(err); reason != want {
		t.Fatalf("rejection = %q (%v), want %q", reason, err, want)
	}
	if current := d.leases.Current(); current != nil {
		t.Fatalf("a lease was acquired before trust verification: %+v", current)
	}
	entries, readErr := os.ReadDir(d.cfg.StateDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if entry.Name() != trustset.StateDirectory {
			t.Fatalf("job state %q was created before trust verification", entry.Name())
		}
	}
}

func TestJobSignedByAKeyInTheDeliveredTrustSetIsAcceptedAndTheSetPersists(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), false, nil)
	set := signTrustTestSet(t, root, 1, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", packKey, &set)); err != nil {
		t.Fatalf("valid trust chain rejected: %v", err)
	}
	reopened, err := trustset.OpenStore(d.cfg.StateDir, trustTestRoots(root))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := reopened.Snapshot(); snapshot.HighestSequence != 1 || snapshot.Current == nil {
		t.Fatalf("the delivered set was not persisted: %+v", snapshot)
	}
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", packKey, nil)); err != nil {
		t.Fatalf("a later job without a set lost the applied trust: %v", err)
	}
}

func TestRotationToANewPackKeyNeedsNoRestartOrHostAction(t *testing.T) {
	root, first, second := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), false, nil)
	setN := signTrustTestSet(t, root, 4, []trustTestSetKey{{id: "pack-key-1", key: first}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", first, &setN)); err != nil {
		t.Fatal(err)
	}
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-2", second, &setN)); commandRejectionReason(err) != v1.CommandAckReasonAgentPackKeyUntrusted {
		t.Fatalf("a key outside the applied set was trusted: %v", err)
	}
	setNext := signTrustTestSet(t, root, 5, []trustTestSetKey{{id: "pack-key-1", key: first}, {id: "pack-key-2", key: second}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-2", second, &setNext)); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", first, nil)); err != nil {
		t.Fatalf("the previous key in the new set was rejected: %v", err)
	}
}

func TestRevokedPackKeyIsRefusedEvenWhenTheServerSendsTheJob(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), true, map[string]string{"pack-key-1": packKey.spki})
	setN := signTrustTestSet(t, root, 1, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", packKey, &setN)); err != nil {
		t.Fatal(err)
	}
	revoking := signTrustTestSet(t, root, 3, nil, "pack-key-1")
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", packKey, &revoking), v1.CommandAckReasonAgentPackKeyRevoked)
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", packKey, &setN), v1.CommandAckReasonAgentPackKeyRevoked)
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", packKey, nil), v1.CommandAckReasonAgentPackKeyRevoked)
}

func TestUnverifiableTrustSetFailsTheJobBeforeAnyWork(t *testing.T) {
	root, foreignRoot, packKey := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	foreign := signTrustTestSet(t, foreignRoot, 1, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	tampered := signTrustTestSet(t, root, 1, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	document, _ := base64.StdEncoding.DecodeString(tampered.DocumentBase64)
	tampered.DocumentBase64 = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(document), `"sequence":1`, `"sequence":9`, 1)))
	malformed := v1.AgentPackTrustedKeySet{DocumentBase64: "not base64", SignatureBase64: "AAAA", RootKeyID: root.keyID}
	for name, set := range map[string]v1.AgentPackTrustedKeySet{"foreign root": foreign, "tampered": tampered, "malformed": malformed} {
		t.Run(name, func(t *testing.T) {
			d := trustTestDaemon(t, trustTestRoots(root), true, map[string]string{"pack-key-1": packKey.spki})
			assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", packKey, &set), v1.CommandAckReasonAgentPackTrustUnverifiable)
			if snapshot := d.agentPackTrust.store.Snapshot(); snapshot.Current != nil || snapshot.HighestSequence != 0 {
				t.Fatalf("an unverifiable set was applied: %+v", snapshot)
			}
		})
	}
}

func TestStaleTrustSetWithoutTheJobKeyIsUnverifiable(t *testing.T) {
	root, first, second := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), false, nil)
	newer := signTrustTestSet(t, root, 7, []trustTestSetKey{{id: "pack-key-2", key: second}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-2", second, &newer)); err != nil {
		t.Fatal(err)
	}
	stale := signTrustTestSet(t, root, 6, []trustTestSetKey{{id: "pack-key-1", key: first}})
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", first, &stale), v1.CommandAckReasonAgentPackTrustUnverifiable)
	if snapshot := d.agentPackTrust.store.Snapshot(); snapshot.HighestSequence != 7 {
		t.Fatalf("a stale set replaced the persisted one: %+v", snapshot)
	}
}

func TestPackKeyOutsideItsValidityWindowIsUnverifiable(t *testing.T) {
	root, early, late := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), false, nil)
	expired := trustTestEpoch.Add(time.Hour)
	set := signTrustTestSet(t, root, 1, []trustTestSetKey{
		{id: "pack-key-expired", key: early, notAfter: &expired},
		{id: "pack-key-future", key: late, notBefore: trustTestEpoch.Add(30 * 24 * time.Hour)},
	})
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-expired", early, &set), v1.CommandAckReasonAgentPackTrustUnverifiable)
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-future", late, &set), v1.CommandAckReasonAgentPackTrustUnverifiable)
}

func TestHandWrittenPackKeysAreOnlyHonouredByDevelopmentBuilds(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	developmentKeys := map[string]string{"pack-key-1": packKey.spki}
	development := trustTestDaemon(t, trustTestRoots(root), true, developmentKeys)
	if err := development.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", packKey, nil)); err != nil {
		t.Fatalf("development build ignored its hand-written key: %v", err)
	}
	release := trustTestDaemon(t, trustTestRoots(root), false, developmentKeys)
	assertTrustRejection(t, release, packJobSignedBy(t, "pack-key-1", packKey, nil), v1.CommandAckReasonAgentPackKeyUntrusted)
}

func TestMissingTrustStateNeverHonoursHandWrittenPackKeys(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	developmentKeys := map[string]string{"pack-key-1": packKey.spki}
	d := trustTestDaemon(t, trustTestRoots(root), true, developmentKeys)
	d.agentPackTrust = nil
	if _, err := d.trustedAgentPackKey(packJobSignedBy(t, "pack-key-1", packKey, nil), developmentKeys); commandRejectionReason(err) != v1.CommandAckReasonAgentPackKeyUntrusted {
		t.Fatalf("rejection = %q (%v), want %q", commandRejectionReason(err), err, v1.CommandAckReasonAgentPackKeyUntrusted)
	}
}

func TestPersistedSetThatNoLongerVerifiesFailsClosed(t *testing.T) {
	oldRoot, newRoot, packKey := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(oldRoot), true, map[string]string{"pack-key-1": packKey.spki})
	set := signTrustTestSet(t, oldRoot, 2, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	if err := d.verifyJobAgentPackAuthority(packJobSignedBy(t, "pack-key-1", packKey, &set)); err != nil {
		t.Fatal(err)
	}
	store, err := trustset.OpenStore(d.cfg.StateDir, trustTestRoots(newRoot))
	if err != nil {
		t.Fatal(err)
	}
	d.agentPackTrust = &agentPackTrust{store: store, developmentKeysAllowed: true}
	assertTrustRejection(t, d, packJobSignedBy(t, "pack-key-1", packKey, nil), v1.CommandAckReasonAgentPackTrustUnverifiable)
}

func writeTrustOverride(t *testing.T, roots ...trustTestKey) string {
	t.Helper()
	entries := make([]map[string]string, 0, len(roots))
	for _, root := range roots {
		entries = append(entries, map[string]string{"keyId": root.keyID, "publicKeySpkiBase64": root.spki})
	}
	raw, err := json.Marshal(map[string]any{"format": 1, "roots": entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "roots.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withReleaseChannel(t *testing.T, channel string) {
	t.Helper()
	previous := Channel
	Channel = channel
	t.Cleanup(func() { Channel = previous })
}

func TestOpenAgentPackTrustAppliesTheConfiguredSetAtStartup(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	t.Setenv(trustroot.OverrideEnvironment, writeTrustOverride(t, root))
	withReleaseChannel(t, "")
	set := signTrustTestSet(t, root, 3, []trustTestSetKey{{id: "pack-key-1", key: packKey}})
	cfg := config.Config{StateDir: t.TempDir(), AgentPackTrustedKeySet: &set}
	trust, err := openAgentPackTrust(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !trust.developmentKeysAllowed || trust.store.Roots().Source != trustroot.SourceDevelopmentOverride {
		t.Fatalf("development trust = %+v", trust)
	}
	if err := trust.applyConfigured(cfg.AgentPackTrustedKeySet); err != nil {
		t.Fatal(err)
	}
	if snapshot := trust.store.Snapshot(); snapshot.HighestSequence != 3 || snapshot.Current == nil {
		t.Fatalf("configured set not applied: %+v", snapshot)
	}
	foreign := signTrustTestSet(t, newTrustTestKey(t), 9, nil)
	if err := trust.applyConfigured(&foreign); err == nil {
		t.Fatal("an unverifiable configured set was applied")
	}
	if err := trust.applyConfigured(nil); err != nil {
		t.Fatalf("an absent configured set failed: %v", err)
	}
}

func TestOpenAgentPackTrustRefusesDevelopmentTrustOnReleaseBuilds(t *testing.T) {
	root, packKey := newTrustTestKey(t), newTrustTestKey(t)
	withReleaseChannel(t, "production")
	t.Setenv(trustroot.OverrideEnvironment, "")
	if _, err := openAgentPackTrust(config.Config{StateDir: t.TempDir(), AgentPackPublicKeys: map[string]string{"pack-key-1": packKey.spki}}); err == nil ||
		!strings.Contains(err.Error(), "development") {
		t.Fatalf("a production build accepted hand-written pack keys: %v", err)
	}
	t.Setenv(trustroot.OverrideEnvironment, writeTrustOverride(t, root))
	if _, err := openAgentPackTrust(config.Config{StateDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a production build accepted the development root override: %v", err)
	}
	t.Setenv(trustroot.OverrideEnvironment, "")
	trust, err := openAgentPackTrust(config.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if trust.developmentKeysAllowed || trust.store.Roots().Channel != trustroot.ChannelProduction {
		t.Fatalf("production trust = %+v", trust)
	}
}

func TestOpenAgentPackTrustRefusesACorruptState(t *testing.T) {
	withReleaseChannel(t, "")
	t.Setenv(trustroot.OverrideEnvironment, "")
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, trustset.StateDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, trustset.StateDirectory, trustset.StateFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAgentPackTrust(config.Config{StateDir: stateDir}); err == nil {
		t.Fatal("a corrupt platform trust state was accepted")
	}
}

func TestCapabilityReportsTheRootsAndTheCurrentlyTrustedKeys(t *testing.T) {
	root, current, expired, development := newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t), newTrustTestKey(t)
	d := trustTestDaemon(t, trustTestRoots(root), true, nil)
	until := trustTestEpoch.Add(time.Hour)
	set := signTrustTestSet(t, root, 1, []trustTestSetKey{{id: "pack-key-current", key: current}, {id: "pack-key-expired", key: expired, notAfter: &until}}, "pack-key-old")
	if err := d.agentPackTrust.applyConfigured(&set); err != nil {
		t.Fatal(err)
	}
	roots, trusted := d.agentPackTrustCapability(map[string]string{"pack-key-dev": development.spki})
	if len(roots) != 1 || roots[0] != root.keyID {
		t.Fatalf("root ids = %v", roots)
	}
	if strings.Join(trusted, ",") != "pack-key-current,pack-key-dev" {
		t.Fatalf("trusted ids = %v", trusted)
	}
	d.agentPackTrust.developmentKeysAllowed = false
	if _, trusted := d.agentPackTrustCapability(map[string]string{"pack-key-dev": development.spki}); strings.Join(trusted, ",") != "pack-key-current" {
		t.Fatalf("release build advertised a hand-written key: %v", trusted)
	}
}
