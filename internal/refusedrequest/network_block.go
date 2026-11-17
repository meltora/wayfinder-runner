package refusedrequest

import (
	"net"
	"regexp"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	unidentifiedDestinationSubject = "unidentified destination"
	maxTrackedCommands             = 256
	maxScannedResultBytes          = 64 << 10
)

type NetworkLayers struct {
	AgentSandbox        bool
	JobEgress           bool
	EgressRulesVerified bool
	ApprovedHosts       []string
}

type NetworkBlock struct {
	Enforcer v1.RefusalEnforcer
	Subject  string
}

type NetworkBlockDetector struct {
	layers   NetworkLayers
	commands map[string]string
}

var (
	toolUseIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	commandURLPattern   = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s'"<>]+`)
	sandboxProxyMarkers = []string{"Connection blocked by network allowlist", "blocked-by-allowlist"}
	hostFailurePatterns = []*regexp.Regexp{
		regexp.MustCompile(`Could not resolve host: ([A-Za-z0-9.-]+)`),
		regexp.MustCompile(`getaddrinfo (?:ENOTFOUND|EAI_AGAIN) ([A-Za-z0-9.-]+)`),
		regexp.MustCompile(`Failed to connect to ([A-Za-z0-9.-]+) port [0-9]+`),
	}
	genericFailureMarkers = []string{
		"Temporary failure in name resolution",
		"Name or service not known",
		"Network is unreachable",
		"connect ENETUNREACH",
		"CONNECT tunnel failed",
	}
)

func NewNetworkBlockDetector(provider string, layers NetworkLayers) *NetworkBlockDetector {
	if provider != "claude" || (!layers.AgentSandbox && !layers.JobEgress) {
		return nil
	}
	layers.ApprovedHosts = append([]string{}, layers.ApprovedHosts...)
	return &NetworkBlockDetector{layers: layers, commands: map[string]string{}}
}

func (d *NetworkBlockDetector) Observe(event map[string]any) []NetworkBlock {
	if d == nil {
		return nil
	}
	switch eventType, _ := event["type"].(string); eventType {
	case "assistant":
		d.trackCommands(event)
		return nil
	case "user":
		return d.inspectResults(event)
	default:
		return nil
	}
}

func (d *NetworkBlockDetector) trackCommands(event map[string]any) {
	for _, block := range contentBlocks(event, "tool_use") {
		id, ok := toolUseID(block["id"])
		if !ok || block["name"] != "Bash" {
			continue
		}
		if _, tracked := d.commands[id]; !tracked && len(d.commands) >= maxTrackedCommands {
			continue
		}
		input, _ := block["input"].(map[string]any)
		d.commands[id] = commandHost(input["command"])
	}
}

func (d *NetworkBlockDetector) inspectResults(event map[string]any) []NetworkBlock {
	var blocks []NetworkBlock
	for _, block := range contentBlocks(event, "tool_result") {
		id, ok := toolUseID(block["tool_use_id"])
		if !ok {
			continue
		}
		hint, tracked := d.commands[id]
		if !tracked {
			continue
		}
		delete(d.commands, id)
		if found, ok := d.classify(resultText(block["content"]), hint); ok {
			blocks = append(blocks, found)
		}
	}
	return blocks
}

func (d *NetworkBlockDetector) classify(text, hint string) (NetworkBlock, bool) {
	host, matched := failureHost(text)
	if host == "" {
		host = hint
	}
	if host != "" && loopbackHost(host) {
		return NetworkBlock{}, false
	}
	if d.layers.AgentSandbox && containsAny(text, sandboxProxyMarkers) {
		return NetworkBlock{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: subjectFor(host)}, true
	}
	if !matched && !containsAny(text, genericFailureMarkers) {
		return NetworkBlock{}, false
	}
	enforcer, ok := d.failureEnforcer(host)
	if !ok {
		return NetworkBlock{}, false
	}
	return NetworkBlock{Enforcer: enforcer, Subject: subjectFor(host)}, true
}

func (d *NetworkBlockDetector) failureEnforcer(host string) (v1.RefusalEnforcer, bool) {
	if host != "" && d.approved(host) {
		if d.layers.JobEgress && !d.layers.EgressRulesVerified {
			return v1.RefusalEnforcerJobEgressRules, true
		}
		return "", false
	}
	switch {
	case d.layers.AgentSandbox:
		return v1.RefusalEnforcerAgentSandbox, true
	case d.layers.JobEgress:
		return v1.RefusalEnforcerJobEgressRules, true
	default:
		return "", false
	}
}

func (d *NetworkBlockDetector) approved(host string) bool {
	for _, pattern := range d.layers.ApprovedHosts {
		pattern = strings.ToLower(pattern)
		if pattern == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(pattern, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func failureHost(text string) (string, bool) {
	matched := false
	for _, pattern := range hostFailurePatterns {
		match := pattern.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		matched = true
		if host, ok := urlHost("http://" + match[1]); ok {
			return host, true
		}
	}
	return "", matched
}

func commandHost(value any) string {
	command, _ := value.(string)
	if len(command) > maxScannedResultBytes {
		command = command[:maxScannedResultBytes]
	}
	for _, candidate := range commandURLPattern.FindAllString(command, 8) {
		if host, ok := urlHost(candidate); ok {
			return host
		}
	}
	return ""
}

func loopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func subjectFor(host string) string {
	if host == "" {
		return unidentifiedDestinationSubject
	}
	return host
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func toolUseID(value any) (string, bool) {
	id, ok := value.(string)
	return id, ok && toolUseIDPattern.MatchString(id)
}

func contentBlocks(event map[string]any, blockType string) []map[string]any {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	blocks := make([]map[string]any, 0, len(content))
	for _, item := range content {
		if block, ok := item.(map[string]any); ok && block["type"] == blockType {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func resultText(content any) string {
	var builder strings.Builder
	appendBounded := func(text string) {
		remaining := maxScannedResultBytes - builder.Len()
		if remaining <= 0 {
			return
		}
		if len(text) > remaining {
			text = text[:remaining]
		}
		builder.WriteString(text)
	}
	switch value := content.(type) {
	case string:
		appendBounded(value)
	case []any:
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok || block["type"] != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				appendBounded(text)
				appendBounded("\n")
			}
		}
	}
	return builder.String()
}
