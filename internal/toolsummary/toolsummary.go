package toolsummary

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/example/app-runner/internal/mcpbridge"
)

const (
	MaxTrackedIDs  = 512
	MaxNamedBlocks = 8
	unlistedName   = "unlisted tool"
	unknownName    = "unknown tool"
	platformPrefix = "mcp__platform__"
)

var toolIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var builtinNames = map[string]struct{}{
	"Agent": {}, "Bash": {}, "BashOutput": {}, "CronCreate": {}, "CronDelete": {}, "CronList": {},
	"Edit": {}, "EnterWorktree": {}, "ExitPlanMode": {}, "ExitWorktree": {}, "Glob": {}, "Grep": {},
	"KillBash": {}, "KillShell": {}, "LS": {}, "ListAgents": {}, "MultiEdit": {}, "NotebookEdit": {},
	"NotebookRead": {}, "Read": {}, "ReportFindings": {}, "ScheduleWakeup": {}, "SendMessage": {},
	"Skill": {}, "SlashCommand": {}, "Task": {}, "TaskOutput": {}, "TaskStop": {}, "TodoWrite": {},
	"ToolSearch": {}, "WebFetch": {}, "WebSearch": {}, "Workflow": {}, "Write": {},
}

var platformNames = func() map[string]struct{} {
	names := map[string]struct{}{}
	for _, definition := range mcpbridge.ToolDefinitions() {
		names[platformPrefix+definition.Name] = struct{}{}
	}
	return names
}()

type Summary struct {
	Text                      string
	Tools                     string
	Truncated                 bool
	CommandArgumentRedactions int
	ToolOutputRedactions      int
}

type Tracker struct {
	names map[string]string
}

func NewTracker() *Tracker {
	return &Tracker{names: map[string]string{}}
}

func (t *Tracker) record(id any, name string) {
	key, ok := validID(id)
	if t == nil || !ok {
		return
	}
	if _, exists := t.names[key]; !exists && len(t.names) >= MaxTrackedIDs {
		return
	}
	t.names[key] = name
}

func (t *Tracker) lookup(id any) string {
	key, ok := validID(id)
	if t == nil || !ok {
		return unknownName
	}
	if name, exists := t.names[key]; exists {
		return name
	}
	return unknownName
}

func validID(id any) (string, bool) {
	text, ok := id.(string)
	if !ok || !toolIDPattern.MatchString(text) {
		return "", false
	}
	return text, true
}

func AllowedName(name string) bool {
	if _, ok := builtinNames[name]; ok {
		return true
	}
	_, ok := platformNames[name]
	return ok
}

func Summarize(event map[string]any, tracker *Tracker) (Summary, bool) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "assistant":
		return summarizeBlocks(toolBlocks(event, "tool_use"), "Tool call: ", "Tool calls: ", func(block map[string]any, summary *Summary) (string, string) {
			if _, ok := block["input"]; ok {
				summary.CommandArgumentRedactions++
			}
			rawName, _ := block["name"].(string)
			name := rawName
			if !AllowedName(rawName) {
				name = unlistedName
				summary.ToolOutputRedactions++
			}
			tracker.record(block["id"], name)
			return name, name
		})
	case "user":
		return summarizeBlocks(toolBlocks(event, "tool_result"), "Tool result: ", "Tool results: ", func(block map[string]any, summary *Summary) (string, string) {
			if _, ok := block["content"]; ok {
				summary.ToolOutputRedactions++
			}
			name := tracker.lookup(block["tool_use_id"])
			outcome := resultOutcome(block["is_error"])
			return name + " " + outcome, name + ":" + outcome
		})
	default:
		return Summary{}, false
	}
}

func summarizeBlocks(blocks []map[string]any, single, plural string, describe func(map[string]any, *Summary) (string, string)) (Summary, bool) {
	if len(blocks) == 0 {
		return Summary{}, false
	}
	var summary Summary
	shown := make([]string, 0, MaxNamedBlocks)
	tools := make([]string, 0, MaxNamedBlocks)
	for index, block := range blocks {
		text, tool := describe(block, &summary)
		if index < MaxNamedBlocks {
			shown = append(shown, text)
			tools = append(tools, tool)
		}
	}
	prefix := single
	if len(blocks) > 1 {
		prefix = plural
	}
	summary.Text = prefix + strings.Join(shown, ", ")
	if extra := len(blocks) - MaxNamedBlocks; extra > 0 {
		summary.Text += " (+" + strconv.Itoa(extra) + " more)"
		summary.Truncated = true
	}
	summary.Tools = strings.Join(tools, ",")
	return summary, true
}

func toolBlocks(event map[string]any, blockType string) []map[string]any {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	blocks := make([]map[string]any, 0, len(content))
	for _, item := range content {
		block, ok := item.(map[string]any)
		if ok && block["type"] == blockType {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func resultOutcome(isError any) string {
	value, ok := isError.(bool)
	switch {
	case !ok:
		return "returned"
	case value:
		return "failed"
	default:
		return "succeeded"
	}
}
