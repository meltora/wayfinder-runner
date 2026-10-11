package provider

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/safeio"
)

const (
	planCommandGuardName           = "plan-command-guard"
	planCommandGuardTimeoutSeconds = 30
	claudePlanTools                = "Read,Grep,Glob,Bash,Skill"
)

var claudePlanAllowedTools = []string{"mcp__platform__*", "Read", "Grep", "Glob", "Skill"}

var claudePlanDeniedTools = []string{
	"Edit", "Write", "NotebookEdit", "MultiEdit", "WebFetch", "WebSearch",
	"Bash(find *-delete*)", "Bash(find *-exec*)", "Bash(find *-ok*)", "Bash(find *-fprint*)", "Bash(find *-fls*)",
	"Bash(git *--output*)", "Bash(git *--ext-diff*)", "Bash(git *--textconv*)",
	"Bash(rg *--pre*)", "Bash(rg *--hostname-bin*)",
}

func claudePlanToolArgs() []string {
	args := []string{"--tools", claudePlanTools, "--allowedTools"}
	args = append(args, claudePlanAllowedTools...)
	args = append(args, "--disallowedTools")
	return append(args, claudePlanDeniedTools...)
}

func claudeSettingSourceArgs(readOnly bool) []string {
	if readOnly {
		return []string{"--restricted", "--setting-sources", ""}
	}
	return []string{"--setting-sources", "user"}
}

func claudePlanPermissionArgs() []string {
	return []string{"--permission-mode", "dontAsk", "--permission-prompts", "none"}
}

func planCommandGuardRoots(p StartParams) ([]string, error) {
	roots := []string{p.WorkDir}
	if p.PackDir != "" {
		roots = append(roots, p.PackDir)
	}
	for _, path := range append([]string{p.ConfigDir, p.mcpBin()}, roots...) {
		if strings.ContainsAny(path, "\x00\r\n") || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("planning command guard paths must be absolute and free of control characters")
		}
	}
	return roots, nil
}

func writePlanCommandGuard(p StartParams) (string, error) {
	roots, err := planCommandGuardRoots(p)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nexec " + shellQuote(p.mcpBin()) + " " + planCommandGuardName)
	for _, root := range roots {
		script.WriteString(" --root " + shellQuote(root))
	}
	script.WriteString("\n")
	if err := safeio.AtomicWriteFile(p.ConfigDir, planCommandGuardName, []byte(script.String()), 0o500); err != nil {
		return "", fmt.Errorf("write planning command guard: %w", err)
	}
	return shellQuote(filepath.Join(p.ConfigDir, planCommandGuardName)), nil
}

func withPlanCommandGuard(p StartParams, hooks map[string]any) (map[string]any, error) {
	command, err := writePlanCommandGuard(p)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(hooks)+1)
	for event, entries := range hooks {
		merged[event] = entries
	}
	existing, _ := merged["PreToolUse"].([]map[string]any)
	merged["PreToolUse"] = append(append([]map[string]any(nil), existing...), map[string]any{
		"matcher": "Bash",
		"hooks": []map[string]any{{
			"type":    "command",
			"command": command,
			"timeout": planCommandGuardTimeoutSeconds,
		}},
	})
	return merged, nil
}
