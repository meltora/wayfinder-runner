package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSourceVersionIsTheDeclaredReleaseVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "release-version"))
	if err != nil {
		t.Fatal(err)
	}
	declared := string(raw)
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\n$`).MatchString(declared) {
		t.Fatalf("packaging/release-version must hold one X.Y.Z line, got %q", declared)
	}
	if Version+"\n" != declared {
		t.Fatalf("source build version %q differs from the declared release version %q", Version, declared[:len(declared)-1])
	}
}
