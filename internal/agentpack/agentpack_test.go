package agentpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func writableCleanup(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
}

func makePack(t *testing.T, key *ecdsa.PrivateKey) (archive []byte, digest string) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "overlays", "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "overlays", "codex", "AGENTS.md"), []byte("codex overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	d, err := PackDir(src, "1.0.0", "codex", &buf, key)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	return buf.Bytes(), d
}

func TestRoundTrip(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, digest := makePack(t, key)
	dest := t.TempDir()
	writableCleanup(t, dest)
	pack, err := VerifyAndMount(bytes.NewReader(archive), digest, &key.PublicKey, dest)
	if err != nil {
		t.Fatalf("VerifyAndMount: %v", err)
	}
	if pack.Manifest.Version != "1.0.0" || pack.Manifest.Provider != "codex" {
		t.Fatalf("manifest = %+v", pack.Manifest)
	}
	data, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(data) != "# skill" {
		t.Fatalf("mounted content: %q, %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(dest, "SKILL.md"))
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %o, want 444", info.Mode().Perm())
	}
}

func TestDigestMismatch(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, _ := makePack(t, key)
	_, err := VerifyAndMount(bytes.NewReader(archive), "deadbeef", &key.PublicKey, t.TempDir())
	if err == nil {
		t.Fatal("expected digest mismatch error")
	}
}

func TestBadSignature(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, digest := makePack(t, key)
	_, err := VerifyAndMount(bytes.NewReader(archive), digest, &other.PublicKey, t.TempDir())
	if err == nil {
		t.Fatal("expected signature error")
	}
}

func TestTamperedFile(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	digest, err := PackDir(src, "1.0.0", "", &buf, key)
	if err != nil {
		t.Fatal(err)
	}
	_ = digest
	dest := t.TempDir()
	writableCleanup(t, dest)
	if _, err := VerifyAndMount(bytes.NewReader(buf.Bytes()), "", &key.PublicKey, dest); err != nil {
		t.Fatalf("clean mount: %v", err)
	}
}

func TestPrivateKeyPEMRoundTripAndFilePolicy(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pack-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := PrivateKeyFromPEMFile(path)
	if err != nil || got.D.Cmp(key.D) != 0 {
		t.Fatalf("PrivateKeyFromPEMFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateKeyFromPEMFile(path); err == nil {
		t.Fatal("public key file permissions accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateKeyFromPEMFile(path); err == nil {
		t.Fatal("invalid PEM accepted")
	}
}

type rawEntry struct {
	name     string
	typeflag byte
	data     []byte
	size     int64
}

func rawPack(t *testing.T, entries ...rawEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := entry.size
		if size == 0 {
			size = int64(len(entry.data))
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: 0o444, Size: size}); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tw.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestVerifyRejectsMalformedArchiveShapes(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := map[string][]byte{
		"not gzip":         []byte("bad"),
		"directory":        rawPack(t, rawEntry{name: "dir", typeflag: tar.TypeDir}),
		"unsafe path":      rawPack(t, rawEntry{name: "../escape", typeflag: tar.TypeReg}),
		"missing metadata": rawPack(t, rawEntry{name: "file", typeflag: tar.TypeReg, data: []byte("x")}),
		"duplicate manifest": rawPack(t,
			rawEntry{name: "pack.yaml", typeflag: tar.TypeReg, data: []byte("version: 1\n")},
			rawEntry{name: "pack.yaml", typeflag: tar.TypeReg, data: []byte("version: 1\n")},
		),
		"unknown manifest field": rawPack(t,
			rawEntry{name: "pack.yaml", typeflag: tar.TypeReg, data: []byte("version: 1\ndigest: deadbeef\nfiles: {a: deadbeef}\nunknown: true\n")},
			rawEntry{name: "signature", typeflag: tar.TypeReg, data: []byte("bad")},
			rawEntry{name: "a", typeflag: tar.TypeReg, data: []byte("x")},
		),
		"multiple documents": rawPack(t,
			rawEntry{name: "pack.yaml", typeflag: tar.TypeReg, data: []byte("version: 1\ndigest: deadbeef\nfiles: {a: deadbeef}\n---\nextra: true\n")},
			rawEntry{name: "signature", typeflag: tar.TypeReg, data: []byte("bad")},
			rawEntry{name: "a", typeflag: tar.TypeReg, data: []byte("x")},
		),
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyAndMount(bytes.NewReader(archive), "", &key.PublicKey, filepath.Join(t.TempDir(), "dest")); err == nil {
				t.Fatal("malformed archive accepted")
			}
		})
	}
}

func TestPackDirRejectsInvalidInputsAndSources(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := PackDir(t.TempDir(), "1", "", nil, key); err == nil {
		t.Fatal("nil output accepted")
	}
	if _, err := PackDir(t.TempDir(), "1", "", &bytes.Buffer{}, nil); err == nil {
		t.Fatal("nil signing key accepted")
	}
	src := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := PackDir(src, "1", "", &bytes.Buffer{}, key); err == nil {
		t.Fatal("symlink source accepted")
	}
}

func TestVerifyRejectsNonEmptyOrSymlinkDestination(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, digest := makePack(t, key)
	nonEmpty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonEmpty, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndMount(bytes.NewReader(archive), digest, &key.PublicKey, nonEmpty); err == nil {
		t.Fatal("non-empty destination accepted")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "dest")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndMount(bytes.NewReader(archive), digest, &key.PublicKey, link); err == nil {
		t.Fatal("symlink destination accepted")
	}
}
