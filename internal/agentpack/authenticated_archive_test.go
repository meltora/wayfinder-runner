package agentpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestMountAuthenticatedArchiveCreatesReadOnlyJobView(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, _ := makePack(t, key)
	destination := filepath.Join(t.TempDir(), "job-view")
	writableCleanup(t, destination)
	pack, err := MountAuthenticatedArchive(bytes.NewReader(archive), destination)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Manifest.Version != "1.0.0" || pack.Manifest.Provider != "codex" {
		t.Fatalf("manifest = %+v", pack.Manifest)
	}
	info, err := os.Stat(filepath.Join(destination, "SKILL.md"))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("file mode=%v err=%v", info, err)
	}
	rootInfo, err := os.Stat(destination)
	if err != nil || rootInfo.Mode().Perm() != 0o555 {
		t.Fatalf("root mode=%v err=%v", rootInfo, err)
	}
}

func TestMountAuthenticatedArchiveRejectsAmbiguousAndDangerousEntries(t *testing.T) {
	cases := map[string][]byte{
		"case collision": rawPack(t,
			rawEntry{name: "A.md", typeflag: tar.TypeReg, data: []byte("a")},
			rawEntry{name: "a.md", typeflag: tar.TypeReg, data: []byte("b")},
		),
		"unicode path": rawPack(t, rawEntry{name: "café.md", typeflag: tar.TypeReg, data: []byte("x")}),
		"deep path":    rawPack(t, rawEntry{name: "a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p/q.md", typeflag: tar.TypeReg, data: []byte("x")}),
		"symlink":      rawPack(t, rawEntry{name: "link", typeflag: tar.TypeSymlink}),
		"fifo":         rawPack(t, rawEntry{name: "pipe", typeflag: tar.TypeFifo}),
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "job-view")
			if _, err := MountAuthenticatedArchive(bytes.NewReader(archive), destination); err == nil {
				t.Fatal("dangerous archive accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("invalid archive created destination: %v", err)
			}
		})
	}
}

func TestMountAuthenticatedArchiveRejectsTrailingPayloadAndSetIDMode(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, _ := makePack(t, key)
	withTrailing := append(append([]byte(nil), archive...), []byte("hidden payload")...)
	if _, err := MountAuthenticatedArchive(bytes.NewReader(withTrailing), filepath.Join(t.TempDir(), "trailing")); err == nil {
		t.Fatal("trailing payload accepted")
	}

	setID := rawPackWithMode(t, "pack.yaml", tar.TypeReg, 0o4644, []byte("x"))
	if _, err := MountAuthenticatedArchive(bytes.NewReader(setID), filepath.Join(t.TempDir(), "setid")); err == nil {
		t.Fatal("set-ID archive mode accepted")
	}
}

func TestMountAuthenticatedArchiveAppliesPolicyBeforeCreatingJobView(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	archive, _ := makePack(t, key)
	for name, policy := range map[string]MountPolicy{
		"version":  {Version: "2.0.0", Provider: "codex"},
		"provider": {Version: "1.0.0", Provider: "opencode"},
	} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "job-view")
			if _, err := MountAuthenticatedArchiveWithPolicy(bytes.NewReader(archive), destination, policy); err == nil {
				t.Fatal("mismatched policy accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("policy failure created job view: %v", err)
			}
		})
	}
}

func rawPackWithMode(t *testing.T, name string, typeflag byte, mode int64, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: typeflag, Mode: mode, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
