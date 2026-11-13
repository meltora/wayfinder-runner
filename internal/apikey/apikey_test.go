package apikey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "test-key-0123456789abcdefghijklmnop"

func TestEnvironmentNameMapsSupportedProviders(t *testing.T) {
	if EnvironmentName("claude") != "ANTHROPIC_API_KEY" || EnvironmentName("codex") != "CODEX_API_KEY" {
		t.Fatal("unexpected provider API key environment names")
	}
	if EnvironmentName("opencode") != "" || EnvironmentName("other") != "" {
		t.Fatal("unsupported providers must not map to an API key variable")
	}
}

func TestResolveReadsNamedRunnerEnvironmentVariable(t *testing.T) {
	t.Setenv("RUNNER_TEST_MODEL_KEY", testKey)
	value, err := Resolve(Source{Env: "RUNNER_TEST_MODEL_KEY"})
	if err != nil || value != testKey {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestResolveRejectsMissingOrMalformedEnvironmentValues(t *testing.T) {
	t.Setenv("RUNNER_TEST_SHORT_KEY", "short")
	t.Setenv("RUNNER_TEST_SPACED_KEY", "has a space in the middle of it")
	for _, name := range []string{"RUNNER_TEST_ABSENT_KEY", "RUNNER_TEST_SHORT_KEY", "RUNNER_TEST_SPACED_KEY"} {
		if _, err := Resolve(Source{Env: name}); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestResolveReadsOwnerOnlySecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anthropic.key")
	if err := os.WriteFile(path, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Resolve(Source{File: path})
	if err != nil || value != testKey {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestResolveRejectsUnsafeSecretFiles(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "readable.key")
	if err := os.WriteFile(readable, []byte(testKey), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(readable, link); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.key")
	if err := os.WriteFile(large, []byte(strings.Repeat("a", MaxLength+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"group readable": readable, "symlink": link, "too large": large,
		"missing": filepath.Join(dir, "missing.key"), "directory": dir,
	} {
		if _, err := Resolve(Source{File: path}); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestResolveErrorsNeverContainTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte(testKey+" trailing words"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Source{File: path})
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err=%v", err)
	}
	t.Setenv("RUNNER_TEST_BAD_KEY", testKey+"\x01")
	_, err = Resolve(Source{Env: "RUNNER_TEST_BAD_KEY"})
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveRequiresExactlyOneSource(t *testing.T) {
	if _, err := Resolve(Source{}); err == nil {
		t.Fatal("empty source accepted")
	}
	if _, err := Resolve(Source{Env: "A", File: "/b"}); err == nil {
		t.Fatal("two sources accepted")
	}
}
