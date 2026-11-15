package elevatedcapability

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

const (
	maxHookGroupsPerEvent  = 8
	maxHookCommandsInGroup = 8
	maxHookTimeoutSeconds  = 600
	maxHookTextBytes       = 4096
)

var hookEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "Notification": true, "UserPromptSubmit": true,
	"Stop": true, "SubagentStop": true, "PreCompact": true, "SessionStart": true, "SessionEnd": true,
	"PermissionRequest": true,
}

var grantableHookEvents = map[string]bool{
	"PostToolUse": true, "Stop": true, "SubagentStop": true, "SessionStart": true, "SessionEnd": true,
	"Notification": true,
}

type hookDeclaration struct {
	Hooks map[string][]json.RawMessage `json:"hooks"`
}

type hookGroup struct {
	Matcher *string           `json:"matcher,omitempty"`
	Hooks   []json.RawMessage `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout *int   `json:"timeout,omitempty"`
}

func IsHookDeclarationPath(key string) bool {
	return strings.HasPrefix(key, ".claude/hooks/") && path.Ext(key) == ".json"
}

func ParseHookDeclaration(content []byte) (map[string][]map[string]any, error) {
	var declaration hookDeclaration
	if err := strictDecode(content, &declaration); err != nil {
		return nil, fmt.Errorf("decode hook declaration: %w", err)
	}
	if len(declaration.Hooks) == 0 {
		return nil, fmt.Errorf("hook declaration names no events")
	}
	hooks := make(map[string][]map[string]any, len(declaration.Hooks))
	for event, rawGroups := range declaration.Hooks {
		if !hookEvents[event] {
			return nil, fmt.Errorf("hook event %q is unsupported", event)
		}
		if len(rawGroups) < 1 || len(rawGroups) > maxHookGroupsPerEvent {
			return nil, fmt.Errorf("hook event %q has an invalid group count", event)
		}
		for _, rawGroup := range rawGroups {
			group, err := parseHookGroup(rawGroup)
			if err != nil {
				return nil, fmt.Errorf("hook event %q: %w", event, err)
			}
			hooks[event] = append(hooks[event], group)
		}
	}
	return hooks, nil
}

func parseHookGroup(raw json.RawMessage) (map[string]any, error) {
	var group hookGroup
	if err := strictDecode(raw, &group); err != nil {
		return nil, err
	}
	if len(group.Hooks) < 1 || len(group.Hooks) > maxHookCommandsInGroup {
		return nil, fmt.Errorf("hook group has an invalid command count")
	}
	commands := make([]map[string]any, 0, len(group.Hooks))
	for _, rawCommand := range group.Hooks {
		var command hookCommand
		if err := strictDecode(rawCommand, &command); err != nil {
			return nil, err
		}
		if command.Type != "command" || strings.TrimSpace(command.Command) == "" || boundedHookText(command.Command) != nil {
			return nil, fmt.Errorf("hook entry must be a non-empty command")
		}
		entry := map[string]any{"type": "command", "command": command.Command}
		if command.Timeout != nil {
			if *command.Timeout < 1 || *command.Timeout > maxHookTimeoutSeconds {
				return nil, fmt.Errorf("hook timeout is out of range")
			}
			entry["timeout"] = *command.Timeout
		}
		commands = append(commands, entry)
	}
	result := map[string]any{"hooks": commands}
	if group.Matcher != nil {
		if err := boundedHookText(*group.Matcher); err != nil {
			return nil, err
		}
		result["matcher"] = *group.Matcher
	}
	return result, nil
}

func boundedHookText(value string) error {
	if len(value) > maxHookTextBytes || strings.ContainsRune(value, 0) {
		return fmt.Errorf("hook text is invalid")
	}
	return nil
}
