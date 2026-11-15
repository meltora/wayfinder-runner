package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/example/app-runner/internal/safeio"
)

const (
	claudePluginViewDirectory = "claude-plugin"
	claudePluginMaxFileBytes  = 1 << 20
	claudePluginMaxTotalBytes = 64 << 20
)

var claudePluginViewComponents = map[string]bool{"skills": true, "commands": true, "agents": true}

func writeClaudePluginView(packDir, configDir string) (string, error) {
	manifestPath := filepath.Join(packDir, ".claude-plugin", "plugin.json")
	if _, err := os.Lstat(manifestPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	raw, err := safeio.ReadRegularFile(manifestPath, claudePluginMaxFileBytes, 0)
	if err != nil {
		return "", fmt.Errorf("read Agent Pack plugin manifest: %w", err)
	}
	manifest, err := sanitizeClaudePluginManifest(raw)
	if err != nil {
		return "", fmt.Errorf("Agent Pack plugin manifest: %w", err)
	}
	files, err := collectClaudePluginViewFiles(packDir)
	if err != nil {
		return "", fmt.Errorf("collect Agent Pack plugin components: %w", err)
	}
	view := filepath.Join(configDir, claudePluginViewDirectory)
	if err := os.RemoveAll(view); err != nil {
		return "", fmt.Errorf("reset Claude plugin view: %w", err)
	}
	if err := os.Mkdir(view, 0o700); err != nil {
		return "", fmt.Errorf("create Claude plugin view: %w", err)
	}
	if err := safeio.AtomicWriteFile(view, filepath.Join(".claude-plugin", "plugin.json"), manifest, 0o400); err != nil {
		return "", fmt.Errorf("write Claude plugin view manifest: %w", err)
	}
	for _, file := range files {
		if err := safeio.AtomicWriteFile(view, filepath.FromSlash(file.name), file.data, file.perm); err != nil {
			return "", fmt.Errorf("write Claude plugin view file %q: %w", file.name, err)
		}
	}
	return view, nil
}

func sanitizeClaudePluginManifest(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("invalid JSON key")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	sanitized := map[string]any{}
	var name string
	if err := json.Unmarshal(fields["name"], &name); err != nil || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name must be a non-empty string")
	}
	sanitized["name"] = name
	for _, key := range []string{"version", "description"} {
		value, present := fields[key]
		if !present {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, fmt.Errorf("%s must be a string", key)
		}
		sanitized[key] = text
	}
	if value, present := fields["author"]; present {
		var author map[string]any
		if err := json.Unmarshal(value, &author); err != nil || author == nil {
			return nil, fmt.Errorf("author must be an object")
		}
		sanitized["author"] = author
	}
	return json.MarshalIndent(sanitized, "", "  ")
}

type claudePluginViewFile struct {
	name string
	data []byte
	perm os.FileMode
}

func collectClaudePluginViewFiles(packDir string) ([]claudePluginViewFile, error) {
	root, err := os.OpenRoot(packDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var files []claudePluginViewFile
	withheld := map[string]bool{}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		component, _, _ := strings.Cut(name, "/")
		if !claudePluginViewComponents[component] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, perm, err := readClaudePluginViewFile(root, name)
		if err != nil {
			return err
		}
		total += len(data)
		if total > claudePluginMaxTotalBytes {
			return fmt.Errorf("plugin components exceed size limit")
		}
		if component != "agents" && strings.EqualFold(path.Ext(name), ".md") && markdownFrontmatterDeclaresHooks(data) {
			withheld[claudePluginViewUnit(name)] = true
		}
		files = append(files, claudePluginViewFile{name: name, data: data, perm: perm})
		return nil
	})
	if err != nil {
		return nil, err
	}
	kept := files[:0]
	for _, file := range files {
		if !withheld[claudePluginViewUnit(file.name)] {
			kept = append(kept, file)
		}
	}
	return kept, nil
}

func claudePluginViewUnit(name string) string {
	parts := strings.SplitN(name, "/", 3)
	if parts[0] == "skills" && len(parts) == 3 {
		return parts[0] + "/" + parts[1]
	}
	return name
}

func readClaudePluginViewFile(root *os.Root, name string) ([]byte, os.FileMode, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > claudePluginMaxFileBytes {
		return nil, 0, fmt.Errorf("plugin file %q must be a regular file within the size limit", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, 0, fmt.Errorf("plugin file %q changed while it was being opened", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, claudePluginMaxFileBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > claudePluginMaxFileBytes {
		return nil, 0, fmt.Errorf("plugin file %q exceeds size limit", name)
	}
	perm := os.FileMode(0o400)
	if opened.Mode().Perm()&0o111 != 0 {
		perm = 0o500
	}
	return data, perm, nil
}

func markdownFrontmatterDeclaresHooks(data []byte) bool {
	text := strings.TrimLeft(strings.TrimPrefix(string(data), byteOrderMark), " \t\r\n")
	if !strings.HasPrefix(text, "---") {
		return false
	}
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], "\r") != "---" {
		return true
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimRight(lines[index], " \t\r") == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return true
	}
	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &frontmatter); err != nil {
		return true
	}
	for key := range frontmatter {
		if strings.EqualFold(strings.TrimSpace(key), "hooks") {
			return true
		}
	}
	return false
}

const byteOrderMark = "\uFEFF"
