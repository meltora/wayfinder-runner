package agentpack

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeProviderManifestAccepted(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("# instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	digest, err := PackDir(src, "1.0.0", "opencode", &archive, key)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	writableCleanup(t, dest)
	pack, err := VerifyAndMount(bytes.NewReader(archive.Bytes()), digest, &key.PublicKey, dest)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Manifest.Provider != "opencode" {
		t.Fatalf("provider = %q", pack.Manifest.Provider)
	}
}
