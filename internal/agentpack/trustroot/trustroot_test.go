package trustroot

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type testRoot struct {
	KeyID               string `json:"keyId"`
	PublicKeySPKIBase64 string `json:"publicKeySpkiBase64"`
}

func generateTestRoot(t *testing.T) testRoot {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return testRoot{KeyID: hex.EncodeToString(sum[:]), PublicKeySPKIBase64: base64.StdEncoding.EncodeToString(der)}
}

func rootsFile(t *testing.T, roots ...testRoot) []byte {
	t.Helper()
	if roots == nil {
		roots = []testRoot{}
	}
	raw, err := json.Marshal(map[string]any{"format": 1, "roots": roots})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func withEmbeddedRoots(t *testing.T, files map[string][]byte) {
	t.Helper()
	previous := embeddedRoots
	mapped := fstest.MapFS{}
	for channel, raw := range files {
		mapped["roots/"+channel+".json"] = &fstest.MapFile{Data: raw}
	}
	embeddedRoots = mapped
	t.Cleanup(func() { embeddedRoots = previous })
}

func TestReleaseChannelMapsSourceBuildsToDevelopment(t *testing.T) {
	cases := map[string]string{"": ChannelDevelopment, "develop": ChannelDevelop, "staging": ChannelStaging, "production": ChannelProduction}
	for daemonChannel, want := range cases {
		got, err := ReleaseChannel(daemonChannel)
		if err != nil || got != want {
			t.Fatalf("ReleaseChannel(%q) = %q, %v; want %q", daemonChannel, got, err, want)
		}
	}
	for _, unknown := range []string{"source", "beta", "Production", "development"} {
		if _, err := ReleaseChannel(unknown); err == nil {
			t.Fatalf("ReleaseChannel(%q) accepted an unknown release channel", unknown)
		}
	}
}

func TestCommittedRootFilesParseForEveryChannel(t *testing.T) {
	for _, channel := range Channels() {
		roots, err := Embedded(channel)
		if err != nil {
			t.Fatalf("embedded roots for %s: %v", channel, err)
		}
		if len(roots.Keys) > MaxRoots || roots.Channel != channel || roots.Source != SourceEmbedded {
			t.Fatalf("embedded roots for %s = %+v", channel, roots)
		}
	}
}

func TestEmbeddedRootsAreSelectedByReleaseChannel(t *testing.T) {
	develop, production, staging, development := generateTestRoot(t), generateTestRoot(t), generateTestRoot(t), generateTestRoot(t)
	withEmbeddedRoots(t, map[string][]byte{
		ChannelDevelopment: rootsFile(t, development),
		ChannelDevelop:     rootsFile(t, develop),
		ChannelStaging:     rootsFile(t, staging),
		ChannelProduction:  rootsFile(t, production),
	})
	roots, err := Resolve("production", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := roots.Find(production.KeyID); !ok {
		t.Fatal("production build does not trust its own root")
	}
	for _, foreign := range []testRoot{develop, staging, development} {
		if _, ok := roots.Find(foreign.KeyID); ok {
			t.Fatalf("production build trusts the foreign root %s", foreign.KeyID)
		}
	}
	if got := roots.KeyIDs(); len(got) != 1 || got[0] != production.KeyID {
		t.Fatalf("production root ids = %v", got)
	}
	developRoots, err := Resolve("develop", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := developRoots.Find(production.KeyID); ok {
		t.Fatal("develop build trusts the production root")
	}
}

func TestEmbeddedRootsReportKeyIDsInSortedOrder(t *testing.T) {
	first, second := generateTestRoot(t), generateTestRoot(t)
	withEmbeddedRoots(t, map[string][]byte{ChannelStaging: rootsFile(t, first, second)})
	roots, err := Embedded(ChannelStaging)
	if err != nil {
		t.Fatal(err)
	}
	ids := roots.KeyIDs()
	if len(ids) != 2 || ids[0] >= ids[1] {
		t.Fatalf("root ids are not sorted: %v", ids)
	}
}

func writeOverride(t *testing.T, raw []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roots.json")
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDevelopmentOverrideReplacesEmbeddedRootsOnlyOnDevelopmentBuilds(t *testing.T) {
	embedded, override := generateTestRoot(t), generateTestRoot(t)
	withEmbeddedRoots(t, map[string][]byte{
		ChannelDevelopment: rootsFile(t, embedded),
		ChannelDevelop:     rootsFile(t, embedded),
		ChannelStaging:     rootsFile(t, embedded),
		ChannelProduction:  rootsFile(t, embedded),
	})
	path := writeOverride(t, rootsFile(t, override), 0o600)
	roots, err := Resolve("", path)
	if err != nil {
		t.Fatal(err)
	}
	if roots.Source != SourceDevelopmentOverride || roots.Channel != ChannelDevelopment {
		t.Fatalf("override roots = %+v", roots)
	}
	if _, ok := roots.Find(override.KeyID); !ok {
		t.Fatal("development override root is not trusted")
	}
	if _, ok := roots.Find(embedded.KeyID); ok {
		t.Fatal("development override must replace the embedded roots")
	}
	for _, channel := range []string{"develop", "staging", "production"} {
		_, err := Resolve(channel, path)
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("Resolve(%q) with an override = %v; want a startup refusal", channel, err)
		}
	}
}

func TestDevelopmentOverrideFileMustBePrivateAndNonEmpty(t *testing.T) {
	root := generateTestRoot(t)
	if _, err := Resolve("", writeOverride(t, rootsFile(t, root), 0o666)); err == nil {
		t.Fatal("world-writable override root file was accepted")
	}
	if _, err := Resolve("", writeOverride(t, rootsFile(t), 0o600)); err == nil {
		t.Fatal("override root file without roots was accepted")
	}
	if _, err := Resolve("", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing override root file was accepted")
	}
}

func TestParseRootsFileRefusesInvalidRoots(t *testing.T) {
	first, second, third := generateTestRoot(t), generateTestRoot(t), generateTestRoot(t)
	mislabelled := first
	mislabelled.KeyID = second.KeyID
	nonEd25519 := testRoot{KeyID: strings.Repeat("a", 64), PublicKeySPKIBase64: base64.StdEncoding.EncodeToString([]byte("not a key"))}
	cases := map[string][]byte{
		"not json":         []byte("{"),
		"unknown format":   []byte(`{"format":2,"roots":[]}`),
		"unknown field":    []byte(`{"format":1,"roots":[],"extra":true}`),
		"duplicate field":  []byte(`{"format":1,"format":1,"roots":[]}`),
		"missing roots":    []byte(`{"format":1}`),
		"too many roots":   rootsFile(t, first, second, third),
		"duplicate root":   rootsFile(t, first, first),
		"mislabelled root": rootsFile(t, mislabelled),
		"non-Ed25519 root": rootsFile(t, nonEd25519),
		"trailing data":    append(rootsFile(t, first), []byte(" {}")...),
	}
	for name, raw := range cases {
		if _, err := ParseRootsFile(raw); err == nil {
			t.Fatalf("%s: roots file was accepted", name)
		}
	}
	parsed, err := ParseRootsFile(rootsFile(t, first, second))
	if err != nil || len(parsed) != 2 {
		t.Fatalf("valid roots file = %v, %v", parsed, err)
	}
}
