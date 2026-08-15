package provider

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
)

const managedHookTimeoutSeconds = 2

// ManagedHookCapability describes the lifecycle facts that a provider adapter
// can emit through runner-owned, per-job configuration. Unsupported adapters
// remain observable through process supervision and MCP calls; callers must not
// infer semantic hook events for them.
type ManagedHookCapability struct {
	Supported         bool
	Version           string
	Events            []string
	UnavailableReason string
}

type managedHookDefinition struct {
	ProviderEvent string
	PlatformEvent string
}

var codexManagedHookDefinitions = []managedHookDefinition{
	{ProviderEvent: "SessionStart", PlatformEvent: "session-start"},
	{ProviderEvent: "SessionEnd", PlatformEvent: "session-end"},
	{ProviderEvent: "PreToolUse", PlatformEvent: "pre-tool"},
	{ProviderEvent: "PostToolUse", PlatformEvent: "post-tool"},
	{ProviderEvent: "Stop", PlatformEvent: "stop"},
	{ProviderEvent: "SubagentStop", PlatformEvent: "subagent-stop"},
	{ProviderEvent: "UserPromptSubmit", PlatformEvent: "user-prompt-submit"},
}

var claudeManagedHookDefinitions = []managedHookDefinition{
	{ProviderEvent: "SessionStart", PlatformEvent: "session-start"},
	{ProviderEvent: "SessionEnd", PlatformEvent: "session-end"},
	{ProviderEvent: "PreToolUse", PlatformEvent: "pre-tool"},
	{ProviderEvent: "PostToolUse", PlatformEvent: "post-tool"},
	{ProviderEvent: "PostToolUseFailure", PlatformEvent: "tool-error"},
	{ProviderEvent: "Stop", PlatformEvent: "stop"},
	{ProviderEvent: "SubagentStop", PlatformEvent: "subagent-stop"},
	{ProviderEvent: "Notification", PlatformEvent: "notification"},
	{ProviderEvent: "UserPromptSubmit", PlatformEvent: "user-prompt-submit"},
}

func managedHookCapability(version string, definitions []managedHookDefinition) ManagedHookCapability {
	events := make([]string, len(definitions))
	for i, definition := range definitions {
		events[i] = definition.ProviderEvent
	}
	return ManagedHookCapability{Supported: true, Version: version, Events: events}
}

func configureManagedHookScripts(p StartParams, definitions []managedHookDefinition) (map[string]string, error) {
	if !p.HardenedRuntime {
		return map[string]string{}, nil
	}
	for name, value := range map[string]string{
		"configuration directory": p.ConfigDir,
		"MCP socket":              p.MCPSock,
		"MCP token file":          p.MCPTokenFile,
		"runner binary":           p.mcpBin(),
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("managed hook %s must not contain control characters", name)
		}
	}
	if p.ConfigDir == "" || !filepath.IsAbs(p.ConfigDir) || !filepath.IsAbs(p.MCPSock) || !filepath.IsAbs(p.MCPTokenFile) {
		return nil, fmt.Errorf("managed hooks require runner-owned absolute configuration and MCP paths")
	}
	binary := p.mcpBin()
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("managed hooks require an absolute runner binary path")
	}

	commands := make(map[string]string, len(definitions))
	written := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		path, ok := written[definition.PlatformEvent]
		if !ok {
			name := "managed-hook-" + definition.PlatformEvent
			path = filepath.Join(p.ConfigDir, name)
			content := managedHookScript(binary, p.MCPSock, p.MCPTokenFile, definition.PlatformEvent)
			if err := safeio.AtomicWriteFile(p.ConfigDir, name, []byte(content), 0o500); err != nil {
				return nil, fmt.Errorf("write managed hook %s: %w", definition.PlatformEvent, err)
			}
			written[definition.PlatformEvent] = path
		}
		commands[definition.ProviderEvent] = shellQuote(path)
	}
	return commands, nil
}

func managedHookScript(binary, socketPath, tokenFile, eventType string) string {
	return "#!/bin/sh\n" +
		shellQuote(binary) + " hook-event --socket " + shellQuote(socketPath) +
		" --token-file " + shellQuote(tokenFile) + " --type " + shellQuote(eventType) +
		" </dev/null >/dev/null 2>&1\nexit 0\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func writeCodexManagedHooks(p StartParams) ([]supervisor.ManagedConfigMount, error) {
	commands, err := configureManagedHookScripts(p, codexManagedHookDefinitions)
	if err != nil || !p.HardenedRuntime {
		return nil, err
	}
	var text strings.Builder
	text.WriteString("allow_managed_hooks_only = true\n\n[features]\nhooks = true\n\n[hooks]\nmanaged_dir = ")
	text.WriteString(strconv.Quote(p.ConfigDir))
	text.WriteString("\n")
	for _, definition := range codexManagedHookDefinitions {
		fmt.Fprintf(&text, "\n[[hooks.%s]]\n\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %s\ntimeout = %d\n",
			definition.ProviderEvent,
			definition.ProviderEvent,
			strconv.Quote(commands[definition.ProviderEvent]),
			managedHookTimeoutSeconds,
		)
	}
	const name = "codex-requirements.toml"
	if err := safeio.AtomicWriteFile(p.ConfigDir, name, []byte(text.String()), 0o600); err != nil {
		return nil, fmt.Errorf("write Codex managed hook requirements: %w", err)
	}
	return []supervisor.ManagedConfigMount{{
		Source: filepath.Join(p.ConfigDir, name),
		Target: "/etc/codex/requirements.toml",
	}}, nil
}

func claudeManagedHooks(p StartParams) (map[string]any, error) {
	commands, err := configureManagedHookScripts(p, claudeManagedHookDefinitions)
	if err != nil || !p.HardenedRuntime {
		return nil, err
	}
	events := make([]string, 0, len(commands))
	for event := range commands {
		events = append(events, event)
	}
	sort.Strings(events)
	hooks := make(map[string]any, len(events))
	for _, event := range events {
		hooks[event] = []map[string]any{{
			"hooks": []map[string]any{{
				"type":    "command",
				"command": commands[event],
				"timeout": managedHookTimeoutSeconds,
			}},
		}}
	}
	return hooks, nil
}
