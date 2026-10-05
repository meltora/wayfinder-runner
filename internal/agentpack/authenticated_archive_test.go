package agentpack

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mountableArchive(t *testing.T, provider string) []byte {
	t.Helper()
	content := sampleArchiveContent()
	content.Provider = provider
	archive, _ := writeSampleArchive(t, content)
	return archive
}

func TestMountAuthenticatedArchiveCreatesReadOnlyJobView(t *testing.T) {
	archive := mountableArchive(t, "claude-code")
	destination := filepath.Join(t.TempDir(), "job-view")
	writableCleanup(t, destination)
	pack, err := MountAuthenticatedArchiveWithPolicy(bytes.NewReader(archive), destination, MountPolicy{Version: "1.0.0", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if pack.Manifest.Version != "1.0.0" || pack.Manifest.Provider != "claude-code" || pack.Root != destination || len(pack.Digest) != 64 {
		t.Fatalf("pack = %+v", pack)
	}
	contents, err := os.ReadFile(filepath.Join(destination, ".claude-plugin", "plugin.json"))
	if err != nil || string(contents) != `{"name":"fixture"}`+"\n" {
		t.Fatalf("contents = %q err = %v", contents, err)
	}
	for _, name := range []string{"WORKFLOW.md", "skills/a/SKILL.md"} {
		info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name)))
		if err != nil || info.Mode().Perm() != 0o444 {
			t.Fatalf("%s mode=%v err=%v", name, info, err)
		}
	}
	for _, directory := range []string{".", "skills", "skills/a", ".claude-plugin"} {
		info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(directory)))
		if err != nil || info.Mode().Perm() != 0o555 {
			t.Fatalf("%s mode=%v err=%v", directory, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, ArchiveManifestName)); !os.IsNotExist(err) {
		t.Fatalf("manifest was mounted as a pack file: %v", err)
	}
}

func TestMountAuthenticatedArchiveAcceptsEveryReaderProvider(t *testing.T) {
	for _, provider := range []string{"claude-code", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "job-view")
			writableCleanup(t, destination)
			pack, err := MountAuthenticatedArchiveWithPolicy(bytes.NewReader(mountableArchive(t, provider)), destination, MountPolicy{Version: "1.0.0", Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			if pack.Manifest.Provider != provider {
				t.Fatalf("provider = %q", pack.Manifest.Provider)
			}
		})
	}
}

func TestMountAuthenticatedArchiveRejectsFormatOneWithTypedErrorAndNoWrites(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := makePack(t, key)
	destination := filepath.Join(t.TempDir(), "job-view")
	_, err = MountAuthenticatedArchive(bytes.NewReader(legacy), destination)
	if reason, ok := ArchiveRejectionReasonOf(err); !ok || reason != RejectUnsupportedFormat {
		t.Fatalf("reason = %q %v (%v)", reason, ok, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("format 1 archive created the job view: %v", err)
	}
}

func TestMountAuthenticatedArchiveRejectsCorpusCasesWithoutCreatingJobView(t *testing.T) {
	for _, name := range []string{"tar_type_symlink", "tar_path_dotdot", "tar_collision_case", "gzip_trailing_garbage", "tar_header_mode_setuid", "manifest_digest_mismatch"} {
		t.Run(name, func(t *testing.T) {
			archive, err := os.ReadFile(filepath.Join(conformanceDirectory, name+".tgz"))
			if err != nil {
				t.Fatal(err)
			}
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

func TestMountAuthenticatedArchiveAppliesPolicyBeforeCreatingJobView(t *testing.T) {
	archive := mountableArchive(t, "codex")
	for name, policy := range map[string]MountPolicy{
		"digest":   {Digest: strings.Repeat("0", 64), Version: "1.0.0", Provider: "codex"},
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
	digest := sha256.Sum256(archive)
	matching := MountPolicy{Digest: hex.EncodeToString(digest[:]), Version: "1.0.0", Provider: "codex"}
	mounted := filepath.Join(t.TempDir(), "job-view")
	writableCleanup(t, mounted)
	if _, err := MountAuthenticatedArchiveWithPolicy(bytes.NewReader(archive), mounted, matching); err != nil {
		t.Fatalf("matching policy refused: %v", err)
	}
	mismatch := MountPolicy{Digest: strings.Repeat("0", 64)}
	if _, err := MountAuthenticatedArchiveWithPolicy(bytes.NewReader(archive), filepath.Join(t.TempDir(), "job-view"), mismatch); !errors.Is(err, ErrArchiveDigestMismatch) {
		t.Fatalf("digest mismatch err = %v", err)
	}
	if _, err := MountAuthenticatedArchive(nil, filepath.Join(t.TempDir(), "nil")); err == nil {
		t.Fatal("nil archive accepted")
	}
}

func TestMountAuthenticatedArchiveRefusesNonEmptyDestination(t *testing.T) {
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MountAuthenticatedArchive(bytes.NewReader(mountableArchive(t, "claude-code")), destination); err == nil {
		t.Fatal("non-empty destination accepted")
	}
	if _, err := os.Stat(filepath.Join(destination, "existing")); err != nil {
		t.Fatalf("existing content removed: %v", err)
	}
}
