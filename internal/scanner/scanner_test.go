package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func byPath(inv Inventory, path string) *Item {
	for i := range inv.Items {
		if inv.Items[i].Path == path {
			return &inv.Items[i]
		}
	}
	return nil
}

func TestScanClassifies(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# instructions", 0o644)
	write(t, root, "CLAUDE.md", "# claude", 0o644)
	write(t, root, ".mcp.json", `{"mcpServers":{}}`, 0o644)
	write(t, root, ".opencode/agents/build.md", "# build", 0o644)
	write(t, root, ".opencode/skills/test/SKILL.md", "# skill", 0o644)
	write(t, root, ".opencode/plugins/unsafe.ts", "export default {}", 0o644)
	write(t, root, "opencode.json", `{"mcp":{}}`, 0o644)
	write(t, root, "deploy.sh", "#!/bin/sh\necho hi", 0o755)
	write(t, root, ".env", "TOKEN=abc", 0o644)
	write(t, root, "src/main.go", "package main", 0o644)

	inv, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if it := byPath(inv, "AGENTS.md"); it == nil || it.Classification != ClassReady || it.Type != "instructions" {
		t.Fatalf("AGENTS.md = %+v", it)
	}
	if it := byPath(inv, ".mcp.json"); it == nil || it.Classification != ClassReview {
		t.Fatalf(".mcp.json = %+v", it)
	}
	if it := byPath(inv, filepath.Join(".opencode", "agents", "build.md")); it == nil || it.Classification != ClassReady || it.Type != "agent" {
		t.Fatalf("OpenCode agent = %+v", it)
	}
	if it := byPath(inv, filepath.Join(".opencode", "skills", "test", "SKILL.md")); it == nil || it.Classification != ClassReady || it.Type != "skill" {
		t.Fatalf("OpenCode skill = %+v", it)
	}
	if it := byPath(inv, filepath.Join(".opencode", "plugins", "unsafe.ts")); it == nil || it.Classification != ClassReview {
		t.Fatalf("OpenCode plugin = %+v", it)
	}
	if it := byPath(inv, "opencode.json"); it == nil || it.Classification != ClassReview {
		t.Fatalf("opencode.json = %+v", it)
	}
	if it := byPath(inv, "deploy.sh"); it == nil || it.Classification != ClassReview {
		t.Fatalf("deploy.sh = %+v", it)
	}
	if it := byPath(inv, ".env"); it == nil || it.Classification != ClassRejected {
		t.Fatalf(".env = %+v", it)
	}
	if it := byPath(inv, "src/main.go"); it != nil {
		t.Fatalf("unrelated file should not be inventoried: %+v", it)
	}
}

func TestSecretPatternRejects(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "use api_key = \"test-only-fake-secret-value\" to auth", 0o644)
	inv, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	it := byPath(inv, "AGENTS.md")
	if it == nil || it.Classification != ClassRejected || it.Reason != "secret pattern detected" {
		t.Fatalf("AGENTS.md = %+v", it)
	}
	if it.DigestSHA256 != "" {
		t.Fatal("digest must not be recorded for rejected files")
	}
}

func TestSkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "node_modules/x/AGENTS.md", "nope", 0o644)
	inv, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(inv.Items) != 0 {
		t.Fatalf("expected empty inventory, got %+v", inv.Items)
	}
}

func TestRejectsAllSymlinksAndOversizedInstructions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real.md", "safe", 0o644)
	if err := os.Symlink("real.md", filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "nested/CLAUDE.md", string(make([]byte, maxFileSize+1)), 0o644)
	inv, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if item := byPath(inv, "AGENTS.md"); item == nil || item.Classification != ClassRejected {
		t.Fatalf("symlink classification = %+v", item)
	}
	if item := byPath(inv, filepath.Join("nested", "CLAUDE.md")); item == nil || item.Classification != ClassRejected {
		t.Fatalf("oversized classification = %+v", item)
	}
}
