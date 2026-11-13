package refusedrequest

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/toolsummary"
)

const (
	genericCommandSubject = "shell command"
	mcpToolSubject        = "MCP tool"
	genericToolSubject    = "tool"
	maxDenials            = 64
)

type Item struct {
	Kind    v1.RefusalKind
	Subject string
}

var (
	programPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	hostPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}\.)*[a-z0-9-]{1,63}$`)
	assignment     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

func Extract(provider string, event map[string]any) []Item {
	if provider != "claude" {
		return nil
	}
	if eventType, _ := event["type"].(string); eventType != "result" {
		return nil
	}
	denials, ok := event["permission_denials"].([]any)
	if !ok {
		return nil
	}
	if len(denials) > maxDenials {
		denials = denials[:maxDenials]
	}
	items := make([]Item, 0, len(denials))
	for _, raw := range denials {
		denial, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item, ok := claudeDenial(denial); ok {
			items = append(items, item)
		}
	}
	return items
}

func claudeDenial(denial map[string]any) (Item, bool) {
	name, _ := denial["tool_name"].(string)
	input, _ := denial["tool_input"].(map[string]any)
	switch name {
	case "":
		return Item{}, false
	case "WebFetch":
		host, ok := urlHost(input["url"])
		if !ok {
			return Item{}, false
		}
		return Item{Kind: v1.RefusalNetworkDestination, Subject: host}, true
	case "Bash":
		return Item{Kind: v1.RefusalCommand, Subject: programName(input["command"])}, true
	}
	switch {
	case toolsummary.AllowedName(name):
		return Item{Kind: v1.RefusalTool, Subject: name}, true
	case strings.HasPrefix(name, "mcp__"):
		return Item{Kind: v1.RefusalTool, Subject: mcpToolSubject}, true
	default:
		return Item{Kind: v1.RefusalTool, Subject: genericToolSubject}, true
	}
}

func urlHost(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok || raw == "" || len(raw) > 4096 {
		return "", false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), true
	}
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return "", false
	}
	return host, true
}

func programName(value any) string {
	command, _ := value.(string)
	for _, word := range strings.Fields(command) {
		if assignment.MatchString(word) {
			continue
		}
		base := path.Base(word)
		if programPattern.MatchString(base) && base != "." && base != ".." {
			return base
		}
		return genericCommandSubject
	}
	return genericCommandSubject
}
