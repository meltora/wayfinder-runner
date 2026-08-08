package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateExcludesSecrets(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("runner-key.pem", "SECRET KEY")
	write("runner-cert.pem", "CERT")
	write("journal.db", "DB")
	write("runner-id", "runner-1")
	write("config.yaml", "control_plane_url: wss://x\nunknown_secret: DO_NOT_EXPORT")
	write("repos/big.git/content", "repo data")

	out, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = os.Remove(out) }()

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[hdr.Name] = true
	}
	for _, forbidden := range []string{"runner-key.pem", "runner-cert.pem", "journal.db", "config.yaml", "repos/big.git/content"} {
		if names[forbidden] {
			t.Fatalf("bundle must not contain %s", forbidden)
		}
	}
	if !names["runner-id"] {
		t.Fatalf("bundle missing expected files: %v", names)
	}
}

func TestCreateDoesNotFollowSymlinks(t *testing.T) {
	stateDir := t.TempDir()
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "outside-secret")
	if err := os.WriteFile(secretPath, []byte("DO NOT EXPORT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(stateDir, "diagnostic-link")); err != nil {
		t.Fatal(err)
	}

	out, err := Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(out) })

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "diagnostic-link" {
			t.Fatal("support bundle included a symlink")
		}
	}
}

func TestCreateIncludesOnlySmallRegularAllowlistedFiles(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "runner-id"), []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "paused"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(out) }()
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	if len(names) != 1 || names[0] != "paused" {
		t.Fatalf("bundle names=%v", names)
	}
	if _, err := Create(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing state directory accepted")
	}
}
