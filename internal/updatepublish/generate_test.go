package updatepublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateCatalogFromExactGoReleaserArchives(t *testing.T) {
	dist := t.TempDir()
	amd64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_amd64.tar.gz", "amd64 archive")
	arm64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_arm64.tar.gz", "arm64 archive")
	raw := artifactJSON(t,
		artifactFixture("our-runner-update_1.4.0_linux_arm64.tar.gz", arm64, "linux", "arm64"),
		artifactFixture("our-runner-update_1.4.0_linux_amd64.tar.gz", amd64, "linux", "amd64"),
		map[string]any{"name": "our-runner", "path": filepath.Join(dist, "ignored"), "type": "Binary", "goos": "linux", "goarch": "amd64"},
	)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	manifest, err := Generate(raw, Options{
		DistRoot: dist, BaseURL: "https://github.com/acme/runner/releases/download/v1.4.0",
		Channel: "stable", Sequence: 104, ReleaseID: "v1.4.0", Version: "1.4.0",
		IssuedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), MinRunner: "1.2.0", MinUpdater: "1.0.0",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(manifest.Artifacts) != 2 || manifest.Artifacts[0].Arch != "amd64" || manifest.Artifacts[1].Arch != "arm64" {
		t.Fatalf("artifacts=%+v", manifest.Artifacts)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.OS != "linux" || artifact.Format != "tar.gz" || !strings.HasPrefix(artifact.URL, "https://github.com/acme/runner/releases/download/v1.4.0/") {
			t.Fatalf("artifact=%+v", artifact)
		}
		if artifact.Size <= 0 || len(artifact.SHA256) != 64 {
			t.Fatalf("artifact integrity metadata=%+v", artifact)
		}
	}
}

func TestGenerateRejectsIncompleteOrChangedRelease(t *testing.T) {
	dist := t.TempDir()
	amd64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_amd64.tar.gz", "amd64 archive")
	fixture := artifactFixture("our-runner-update_1.4.0_linux_amd64.tar.gz", amd64, "linux", "amd64")
	opts := Options{
		DistRoot: dist, BaseURL: "https://releases.example.test/v1.4.0", Channel: "stable",
		Sequence: 1, ReleaseID: "v1.4.0", Version: "1.4.0",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour), MinRunner: "1.0.0", MinUpdater: "1.0.0",
	}
	if _, err := Generate(artifactJSON(t, fixture), opts); err == nil || !strings.Contains(err.Error(), "arm64") {
		t.Fatalf("missing platform err=%v", err)
	}

	arm64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_arm64.tar.gz", "arm64 archive")
	changed := artifactFixture("our-runner-update_1.4.0_linux_arm64.tar.gz", arm64, "linux", "arm64")
	extra := changed["extra"].(map[string]any)
	extra["Checksum"] = "sha256:" + strings.Repeat("0", 64)
	if _, err := Generate(artifactJSON(t, fixture, changed), opts); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("changed archive err=%v", err)
	}
}

func TestGenerateRejectsSymlinkAndUnexpectedArtifactName(t *testing.T) {
	dist := t.TempDir()
	target := writeArchiveFixture(t, dist, "target.tar.gz", "archive")
	link := filepath.Join(dist, "our-runner-update_1.4.0_linux_amd64.tar.gz")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	arm64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_arm64.tar.gz", "arm64")
	opts := Options{
		DistRoot: dist, BaseURL: "https://releases.example.test/v1.4.0", Channel: "stable",
		Sequence: 1, ReleaseID: "v1.4.0", Version: "1.4.0",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour), MinRunner: "1.0.0", MinUpdater: "1.0.0",
	}
	if _, err := Generate(artifactJSON(t,
		artifactFixture(filepath.Base(link), link, "linux", "amd64"),
		artifactFixture(filepath.Base(arm64), arm64, "linux", "arm64"),
	), opts); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("symlink err=%v", err)
	}
}

func TestGenerateRejectsInvalidMetadataAndContainment(t *testing.T) {
	dist := t.TempDir()
	amd64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_amd64.tar.gz", "amd64")
	arm64 := writeArchiveFixture(t, dist, "our-runner-update_1.4.0_linux_arm64.tar.gz", "arm64")
	baseRecords := []map[string]any{
		artifactFixture(filepath.Base(amd64), amd64, "linux", "amd64"),
		artifactFixture(filepath.Base(arm64), arm64, "linux", "arm64"),
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	validOptions := Options{
		DistRoot: dist, BaseURL: "https://releases.example.test/v1.4.0", Channel: "stable",
		Sequence: 1, ReleaseID: "v1.4.0", Version: "1.4.0", IssuedAt: now,
		ExpiresAt: now.Add(time.Hour), MinRunner: "1.0.0", MinUpdater: "1.0.0",
	}
	tests := []struct {
		name    string
		raw     []byte
		options Options
	}{
		{name: "invalid base", raw: artifactJSON(t, baseRecords...), options: withBase(validOptions, "http://releases.example.test")},
		{name: "missing dist", raw: artifactJSON(t, baseRecords...), options: withDist(validOptions, "")},
		{name: "trailing json", raw: append(artifactJSON(t, baseRecords...), []byte(` {}`)...), options: validOptions},
		{name: "duplicate platform", raw: artifactJSON(t, baseRecords[0], baseRecords[0], baseRecords[1]), options: validOptions},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Generate(test.raw, test.options); err == nil {
				t.Fatal("invalid release metadata accepted")
			}
		})
	}

	outside := writeArchiveFixture(t, t.TempDir(), filepath.Base(amd64), "outside")
	outsideRecord := artifactFixture(filepath.Base(outside), outside, "linux", "amd64")
	if _, err := Generate(artifactJSON(t, outsideRecord, baseRecords[1]), validOptions); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("outside archive err=%v", err)
	}

	wrongFormat := artifactFixture(filepath.Base(amd64), amd64, "linux", "amd64")
	wrongFormat["extra"].(map[string]any)["Format"] = "zip"
	if _, err := Generate(artifactJSON(t, wrongFormat, baseRecords[1]), validOptions); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("format err=%v", err)
	}
}

func withBase(options Options, base string) Options {
	options.BaseURL = base
	return options
}

func withDist(options Options, dist string) Options {
	options.DistRoot = dist
	return options
}

func writeArchiveFixture(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func artifactFixture(name, path, goos, goarch string) map[string]any {
	raw, _ := os.ReadFile(path)
	digest := sha256.Sum256(raw)
	return map[string]any{
		"name": name, "path": path, "type": "Archive", "goos": goos, "goarch": goarch,
		"extra": map[string]any{"Format": "tar.gz", "ID": "linux-update", "Checksum": "sha256:" + hex.EncodeToString(digest[:])},
	}
}

func artifactJSON(t *testing.T, values ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
