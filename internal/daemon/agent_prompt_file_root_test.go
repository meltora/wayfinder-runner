package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func symlinkedPromptsDirectory(t *testing.T) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "prompt-x"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "supervisor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, promptsDirectory(stateDir)); err != nil {
		t.Fatal(err)
	}
	return stateDir, filepath.Join(outside, "prompt-x")
}

func TestPromptSweepRefusesASymlinkedPromptsDirectory(t *testing.T) {
	stateDir, outsidePrompt := symlinkedPromptsDirectory(t)
	if err := sweepAgentPromptFiles(stateDir); err == nil {
		t.Fatal("sweep followed a symlinked prompts directory")
	}
	if _, err := os.Lstat(outsidePrompt); err != nil {
		t.Fatalf("sweep removed a file outside the prompts directory: %v", err)
	}
}

func TestPromptFileRemoveRefusesASymlinkedPromptsDirectory(t *testing.T) {
	stateDir := t.TempDir()
	file, err := writeAgentPromptFile(stateDir, "attempt-1", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	name := filepath.Base(file.path)
	if err := os.WriteFile(filepath.Join(outside, name), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(promptsDirectory(stateDir), filepath.Join(t.TempDir(), "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, promptsDirectory(stateDir)); err != nil {
		t.Fatal(err)
	}
	if err := file.Remove(); err == nil {
		t.Fatal("remove followed a symlinked prompts directory")
	}
	if _, err := os.Lstat(filepath.Join(outside, name)); err != nil {
		t.Fatalf("remove deleted a file outside the prompts directory: %v", err)
	}
}

func TestPromptSweepKeepsFilesWithoutThePromptPrefix(t *testing.T) {
	stateDir := t.TempDir()
	file, err := writeAgentPromptFile(stateDir, "attempt-1", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(promptsDirectory(stateDir), "operator-notes")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sweepAgentPromptFiles(stateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(file.path); !os.IsNotExist(err) {
		t.Fatalf("sweep left %q: %v", file.path, err)
	}
	if _, err := os.Lstat(keep); err != nil {
		t.Fatalf("sweep removed a file without the prompt prefix: %v", err)
	}
}

func TestPromptSweepAndRemoveIgnoreMissingDirectoriesWithoutCreatingThem(t *testing.T) {
	stateDir := t.TempDir()
	if err := sweepAgentPromptFiles(stateDir); err != nil {
		t.Fatalf("sweep without a supervisor directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "supervisor")); !os.IsNotExist(err) {
		t.Fatalf("sweep created the supervisor directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(stateDir, "supervisor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sweepAgentPromptFiles(stateDir); err != nil {
		t.Fatalf("sweep without a prompts directory: %v", err)
	}
	if _, err := os.Lstat(promptsDirectory(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("sweep created the prompts directory: %v", err)
	}
	file, err := writeAgentPromptFile(t.TempDir(), "attempt-1", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(file.path))); err != nil {
		t.Fatal(err)
	}
	if err := file.Remove(); err != nil {
		t.Fatalf("remove after the supervisor directory vanished: %v", err)
	}
}

func TestPromptSweepRefusesAPromptsEntryThatIsNotADirectory(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "supervisor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptsDirectory(stateDir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sweepAgentPromptFiles(stateDir); err == nil {
		t.Fatal("sweep accepted a prompts entry that is a file")
	}
}
