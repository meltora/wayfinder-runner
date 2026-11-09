package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func blockedStatePath(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(file, "state")
}

func occupyWithDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func leftoverTemps(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			found = append(found, entry.Name())
		}
	}
	return found
}

func TestSetPausedStateRefusesAnUnsafeStateDirectory(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetPausedState(shared, true); err == nil || !strings.Contains(err.Error(), "private real directory") {
		t.Fatalf("shared state directory: %v", err)
	}
	if err := SetPausedState(blockedStatePath(t), true); err == nil || !strings.Contains(err.Error(), "create state directory") {
		t.Fatalf("uncreatable state directory: %v", err)
	}
}

func TestSetPausedStateReportsAPauseEntryItCannotReplace(t *testing.T) {
	dir := privateStateDir(t)
	occupyWithDirectory(t, filepath.Join(dir, "paused"))
	if err := SetPausedState(dir, false); err == nil || !strings.Contains(err.Error(), "remove pause state") {
		t.Fatalf("resume over a non-empty pause entry: %v", err)
	}
	if err := SetPausedState(dir, true); err == nil || !strings.Contains(err.Error(), "commit pause state") {
		t.Fatalf("pause over a non-empty pause entry: %v", err)
	}
	if temps := leftoverTemps(t, dir, ".paused-"); len(temps) != 0 {
		t.Fatalf("failed pause left temporary files: %v", temps)
	}
	if _, err := ReadPausedState(dir); err == nil {
		t.Fatal("a directory pause entry read as a valid pause state")
	}
}

func TestReadPausedStateRefusesASharedOrOversizedFile(t *testing.T) {
	for name, file := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		"shared":    {"1\n", 0o644},
		"oversized": {strings.Repeat("1", 17), 0o600},
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateStateDir(t)
			path := filepath.Join(dir, "paused")
			if err := os.WriteFile(path, []byte(file.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, file.mode); err != nil {
				t.Fatal(err)
			}
			if paused, err := ReadPausedState(dir); err == nil || paused {
				t.Fatalf("paused=%v err=%v", paused, err)
			}
		})
	}
}

func TestReadRunnerIDRefusesOversizedAndInvalidContent(t *testing.T) {
	for name, content := range map[string]string{
		"oversized": strings.Repeat("a", 4097),
		"invalid":   "../runner\n",
		"empty":     "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateStateDir(t)
			if err := os.WriteFile(filepath.Join(dir, "runner-id"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if id, err := ReadRunnerID(dir); err == nil {
				t.Fatalf("runner ID %q accepted", id)
			}
		})
	}
}

func TestWriteRunnerIDRefusesInvalidIDsAndUnsafeStateDirectories(t *testing.T) {
	dir := privateStateDir(t)
	if err := WriteRunnerID(dir, "../runner"); err == nil {
		t.Fatal("invalid runner ID written")
	}
	if _, err := os.Lstat(filepath.Join(dir, "runner-id")); !os.IsNotExist(err) {
		t.Fatalf("an invalid runner ID left a file: %v", err)
	}
	if err := WriteRunnerID(blockedStatePath(t), "runner-1"); err == nil {
		t.Fatal("runner ID written below a regular file")
	}
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(privateStateDir(t), link); err != nil {
		t.Fatal(err)
	}
	if err := WriteRunnerID(link, "runner-1"); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlinked state directory: %v", err)
	}
}

func TestWriteRunnerIDKeepsNoTemporaryFileWhenItCannotCommit(t *testing.T) {
	dir := privateStateDir(t)
	occupyWithDirectory(t, filepath.Join(dir, "runner-id"))
	if err := WriteRunnerID(dir, "runner-1"); err == nil {
		t.Fatal("runner ID replaced a non-empty directory")
	}
	if temps := leftoverTemps(t, dir, ".runner-id-"); len(temps) != 0 {
		t.Fatalf("failed write left temporary files: %v", temps)
	}
	if _, err := ReadRunnerID(dir); err == nil {
		t.Fatal("a directory runner-id read as a valid runner ID")
	}
}
