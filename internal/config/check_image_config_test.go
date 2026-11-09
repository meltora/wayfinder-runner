package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCheckImage = "our-runner-check-go:rdbc@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestCheckImageAcceptsAnEmptyOrDigestPinnedReference(t *testing.T) {
	for name, image := range map[string]string{
		"empty":         "",
		"name and tag":  testCheckImage,
		"registry name": "registry.example.com:5000/tools/go@sha256:" + strings.Repeat("0", 64),
		"uppercase hex": "golang@sha256:" + strings.Repeat("AbCdEf", 10) + "ABCD",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCheck("go-test", CheckConfig{Command: "go", Image: image}); err != nil {
				t.Fatalf("ValidateCheck(%q) = %v", image, err)
			}
		})
	}
}

func TestCheckImageRefusesAReferenceThatIsNotPinnedByDigest(t *testing.T) {
	const want = "check go-test: image must be pinned by digest (name@sha256:<64 hex>)"
	for name, image := range map[string]string{
		"tag only":           "golang:1.26-bookworm",
		"name only":          "golang",
		"whitespace":         " ",
		"leading whitespace": " " + testCheckImage,
		"trailing newline":   testCheckImage + "\n",
		"short digest":       "golang@sha256:" + strings.Repeat("a", 63),
		"long digest":        "golang@sha256:" + strings.Repeat("a", 65),
		"other algorithm":    "golang@sha512:" + strings.Repeat("a", 64),
		"non-hex digest":     "golang@sha256:" + strings.Repeat("g", 64),
		"digest only":        "@sha256:" + strings.Repeat("a", 64),
		"two digests":        "golang@sha256:" + strings.Repeat("a", 64) + "@sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateCheck("go-test", CheckConfig{Command: "go", Image: image})
			if err == nil || err.Error() != want {
				t.Fatalf("ValidateCheck(%q) = %v, want %q", image, err, want)
			}
			cfg := productionConfig()
			cfg.Checks = map[string]CheckConfig{"go-test": {Command: "go", Image: image}}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Validate = %v, want %q", err, want)
			}
		})
	}
}

func TestLoadReadsACheckImageFromTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := "control_plane_url: wss://cp.example.com/stream\ntransport: websocket\n" +
		"job_image: registry.example.com/runner/job@sha256:" + strings.Repeat("a", 64) + "\n" +
		"checks:\n  go-test:\n    command: go\n    args: [test, ./...]\n    image: " + testCheckImage + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Checks["go-test"].Image; got != testCheckImage {
		t.Fatalf("image = %q", got)
	}
	normalized := Normalize(cfg)
	if got := normalized.Checks["go-test"].Image; got != testCheckImage {
		t.Fatalf("normalized image = %q", got)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(raw, testCheckImage, "golang:1.26", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "check go-test: image must be pinned by digest") {
		t.Fatalf("tag-only image from the config file = %v", err)
	}
}
