package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Classification string

const (
	ClassReady    Classification = "ready"
	ClassWarning  Classification = "warning"
	ClassBlocked  Classification = "blocked"
	ClassReview   Classification = "review"
	ClassRejected Classification = "rejected"
)

type Item struct {
	Path           string         `json:"path"`
	Type           string         `json:"type"`
	DigestSHA256   string         `json:"digest_sha256"`
	SizeBytes      int64          `json:"size_bytes"`
	Classification Classification `json:"classification"`
	Reason         string         `json:"reason,omitempty"`
}

type Inventory struct {
	Items []Item `json:"items"`
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*["']?[A-Za-z0-9_\-+/=]{16,}`),
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
}

var rejectedNames = map[string]bool{
	".env": true, ".env.local": true, ".env.production": true,
	"credentials": true, "auth.json": true, ".credentials.json": true,
	"id_rsa": true, "id_ed25519": true, "cookies": true,
}

func classify(path string, info fs.FileInfo) (string, Classification, string) {
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	if rejectedNames[lower] || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
		return "", ClassRejected, "credential-like file"
	}
	if info.Mode()&0o111 != 0 && !strings.HasSuffix(lower, ".md") {
		return "", ClassReview, "executable content requires approval"
	}
	switch {
	case base == "AGENTS.md":
		return "instructions", ClassReady, ""
	case base == "CLAUDE.md":
		return "instructions", ClassReady, ""
	case strings.HasSuffix(path, filepath.Join(".claude", "commands")+string(filepath.Separator)) || strings.Contains(path, filepath.Join(".claude", "commands")):
		return "command", ClassReady, ""
	case strings.Contains(path, filepath.Join(".claude", "skills")) || strings.Contains(path, filepath.Join(".agents", "skills")):
		return "skill", ClassReady, ""
	case strings.Contains(path, filepath.Join(".opencode", "skills")):
		return "skill", ClassReady, ""
	case strings.Contains(path, filepath.Join(".opencode", "agents")):
		return "agent", ClassReady, ""
	case strings.Contains(path, filepath.Join(".opencode", "commands")):
		return "command", ClassReady, ""
	case lower == ".mcp.json" || lower == "opencode.json" || lower == "opencode.jsonc" || strings.Contains(path, filepath.Join(".claude", "settings.json")):
		return "mcp-config", ClassReview, "MCP configuration requires review"
	case strings.Contains(path, filepath.Join(".opencode", "plugins")) || strings.Contains(path, filepath.Join(".opencode", "tools")):
		return "executable-extension", ClassReview, "OpenCode plugins and custom tools require approval"
	case strings.Contains(path, "hooks"):
		return "hook", ClassReview, "hook requires approval"
	}
	return "", "", ""
}

func containsSecret(data []byte) bool {
	for _, p := range secretPatterns {
		if p.Match(data) {
			return true
		}
	}
	return false
}

const maxFileSize = 1 << 20

func Scan(root string) (Inventory, error) {
	var inv Inventory
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return inv, fmt.Errorf("open scan root %s: %w", root, err)
	}
	defer func() { _ = rootHandle.Close() }()
	err = fs.WalkDir(rootHandle.FS(), ".", func(relSlash string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(relSlash)
		if info.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", ".next", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			inv.Items = append(inv.Items, Item{Path: rel, Type: "symlink", Classification: ClassRejected, Reason: "symlinks are not imported"})
			return nil
		}
		typ, class, reason := classify(rel, info)
		if typ == "" && class == "" {
			return nil
		}
		item := Item{Path: rel, Type: typ, Classification: class, Reason: reason, SizeBytes: info.Size()}
		if info.Size() > maxFileSize {
			item.Classification = ClassRejected
			item.Reason = "file exceeds import size limit"
		}
		if item.Classification != ClassRejected && info.Size() <= maxFileSize {
			f, err := rootHandle.Open(rel)
			if err != nil {
				item.Classification = ClassRejected
				item.Reason = "file could not be opened safely"
			} else {
				openedInfo, statErr := f.Stat()
				h := sha256.New()
				data, rerr := io.ReadAll(io.LimitReader(io.TeeReader(f, h), maxFileSize+1))
				_ = f.Close()
				if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || rerr != nil || len(data) > maxFileSize {
					item.Classification = ClassRejected
					item.Reason = "file changed or could not be read safely"
				} else {
					item.DigestSHA256 = hex.EncodeToString(h.Sum(nil))
					if containsSecret(data) {
						item.Classification = ClassRejected
						item.Reason = "secret pattern detected"
						item.DigestSHA256 = ""
					}
				}
			}
		}
		inv.Items = append(inv.Items, item)
		return nil
	})
	if err != nil {
		return inv, fmt.Errorf("scan %s: %w", root, err)
	}
	return inv, nil
}
