package elevatedcapability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
)

const (
	MaxMCPServersPerDeclaration = 16
	maxMCPArguments             = 64
	maxMCPMapEntries            = 64
	maxMCPStringBytes           = 4096
)

var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var reservedMCPServerNames = map[string]bool{"platform": true}

type mcpDeclaration struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func ParseMCPDeclaration(content []byte) (map[string]map[string]any, error) {
	var declaration mcpDeclaration
	if err := strictDecode(content, &declaration); err != nil {
		return nil, fmt.Errorf("decode MCP declaration: %w", err)
	}
	if len(declaration.MCPServers) < 1 || len(declaration.MCPServers) > MaxMCPServersPerDeclaration {
		return nil, fmt.Errorf("MCP declaration must name 1 to %d servers", MaxMCPServersPerDeclaration)
	}
	servers := make(map[string]map[string]any, len(declaration.MCPServers))
	for name, raw := range declaration.MCPServers {
		if !mcpServerNamePattern.MatchString(name) || reservedMCPServerNames[name] {
			return nil, fmt.Errorf("MCP server name %q is not allowed", name)
		}
		var server mcpServer
		if err := strictDecode(raw, &server); err != nil {
			return nil, fmt.Errorf("decode MCP server %q: %w", name, err)
		}
		entry, err := server.entry()
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		servers[name] = entry
	}
	return servers, nil
}

func (server mcpServer) entry() (map[string]any, error) {
	if err := boundedStrings(append([]string{server.Command, server.URL}, server.Args...)...); err != nil {
		return nil, err
	}
	if len(server.Args) > maxMCPArguments || len(server.Env) > maxMCPMapEntries || len(server.Headers) > maxMCPMapEntries {
		return nil, fmt.Errorf("server declaration exceeds limits")
	}
	for _, values := range []map[string]string{server.Env, server.Headers} {
		for key, value := range values {
			if key == "" || boundedStrings(key, value) != nil {
				return nil, fmt.Errorf("server map entry is invalid")
			}
		}
	}
	switch server.Type {
	case "", "stdio":
		if server.Command == "" || server.URL != "" || len(server.Headers) > 0 {
			return nil, fmt.Errorf("stdio server requires a command only")
		}
		entry := map[string]any{"type": "stdio", "command": server.Command, "args": append([]string{}, server.Args...)}
		if len(server.Env) > 0 {
			entry["env"] = server.Env
		}
		return entry, nil
	case "http", "sse":
		parsed, err := url.Parse(server.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 {
			return nil, fmt.Errorf("remote server requires an https URL only")
		}
		entry := map[string]any{"type": server.Type, "url": server.URL}
		if len(server.Headers) > 0 {
			entry["headers"] = server.Headers
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("server type is unsupported")
	}
}

func boundedStrings(values ...string) error {
	for _, value := range values {
		if len(value) > maxMCPStringBytes || bytes.ContainsRune([]byte(value), 0) {
			return fmt.Errorf("server string is invalid")
		}
	}
	return nil
}
