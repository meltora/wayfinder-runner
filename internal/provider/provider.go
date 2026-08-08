package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
)

type StartParams struct {
	Job             v1.JobSpec
	WorkDir         string
	ConfigDir       string
	PackDir         string
	MCPSock         string
	MCPTokenFile    string
	MCPBinary       string
	Prompt          string
	BinaryPath      string
	Model           string
	ResumeSessionID string
	ReadOnly        bool
	HardenedRuntime bool
}

func (p StartParams) mcpBin() string {
	if p.MCPBinary != "" {
		return p.MCPBinary
	}
	bin, err := os.Executable()
	if err != nil {
		return "our-runner"
	}
	return bin
}

type Adapter interface {
	Name() string
	Available() bool
	BuildSpec(p StartParams) (supervisor.Spec, error)
	ParseLine(line []byte) (map[string]any, bool)
}

func mcpServerEntry(socketPath, tokenFile, bin string) map[string]any {
	return map[string]any{
		"command": bin,
		"args":    []string{"mcp-serve", "--socket", socketPath, "--token-file", tokenFile},
	}
}

type Codex struct {
	LookPath func(string) (string, error)
}

func NewCodex() *Codex { return &Codex{} }

func (c *Codex) Name() string { return "codex" }
func (c *Codex) Available() bool {
	if c.LookPath != nil {
		_, err := c.LookPath("codex")
		return err == nil
	}
	_, err := exec_LookPath("codex")
	return err == nil
}

func (c *Codex) BuildSpec(p StartParams) (supervisor.Spec, error) {
	bin := p.BinaryPath
	if bin == "" {
		bin = "codex"
	}
	args := []string{"exec", "--json"}
	if p.HardenedRuntime {
		profile := "runner-build"
		if p.ReadOnly {
			profile = "runner-plan"
		}
		args = append(args, "--profile", profile, "--strict-config")
	} else {
		// codex exec auto-cancels MCP tool calls when stdin is not a TTY, even
		// with approval_policy="never"; the bypass flag is documented for
		// externally sandboxed automation, which the runner provides.
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args,
		"-c", fmt.Sprintf(`mcp_servers.platform.command=%q`, p.mcpBin()),
		"-c", fmt.Sprintf(`mcp_servers.platform.args=["mcp-serve", "--socket", %q, "--token-file", %q]`, p.MCPSock, p.MCPTokenFile),
	)
	if p.PackDir != "" {
		args = append(args, "--add-dir", p.PackDir)
	}
	if p.ResumeSessionID != "" {
		args = append(args, "resume", p.ResumeSessionID)
	}
	args = append(args, p.Prompt)
	return supervisor.Spec{
		Name:    "codex-" + p.Job.AttemptID,
		Command: bin,
		Args:    args,
		Dir:     p.WorkDir,
		Env:     allowedEnvironment(),
	}, nil
}

func (c *Codex) ParseLine(line []byte) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, false
	}
	return m, true
}

type Claude struct {
	LookPath func(string) (string, error)
}

func NewClaude() *Claude { return &Claude{} }

func (c *Claude) Name() string { return "claude" }
func (c *Claude) Available() bool {
	if c.LookPath != nil {
		_, err := c.LookPath("claude")
		return err == nil
	}
	_, err := exec_LookPath("claude")
	return err == nil
}

func (c *Claude) BuildSpec(p StartParams) (supervisor.Spec, error) {
	bin := p.BinaryPath
	if bin == "" {
		bin = "claude"
	}
	if p.ConfigDir == "" || !filepath.IsAbs(p.ConfigDir) {
		return supervisor.Spec{}, fmt.Errorf("Claude job config requires a runner-owned absolute directory")
	}
	mcpCfg := map[string]any{"mcpServers": map[string]any{"platform": mcpServerEntry(p.MCPSock, p.MCPTokenFile, p.mcpBin())}}
	raw, _ := json.MarshalIndent(mcpCfg, "", "  ")
	mcpPath := filepath.Join(p.ConfigDir, "claude-mcp.json")
	if err := safeio.AtomicWriteFile(p.ConfigDir, "claude-mcp.json", raw, 0o600); err != nil {
		return supervisor.Spec{}, err
	}
	settings := map[string]any{
		"permissions": map[string]any{
			"deny": []string{
				"Read(~/.claude/**)", "Edit(~/.claude/**)",
				"Read(~/.claude.json)", "Edit(~/.claude.json)",
			},
		},
		"sandbox": map[string]any{
			"enabled":                  p.HardenedRuntime,
			"failIfUnavailable":        p.HardenedRuntime,
			"allowUnsandboxedCommands": false,
			"filesystem": map[string]any{
				"denyRead":  []string{"~/.claude", "~/.claude.json"},
				"denyWrite": []string{"~/.claude", "~/.claude.json"},
			},
			"credentials": map[string]any{
				"files": []map[string]string{
					{"path": "~/.claude/.credentials.json", "mode": "deny"},
					{"path": "~/.claude.json", "mode": "deny"},
				},
			},
			"network": map[string]any{"strictAllowlist": true, "allowedDomains": []string{}},
		},
	}
	settingsRaw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return supervisor.Spec{}, err
	}
	settingsPath := filepath.Join(p.ConfigDir, "claude-settings.json")
	if err := safeio.AtomicWriteFile(p.ConfigDir, "claude-settings.json", settingsRaw, 0o600); err != nil {
		return supervisor.Spec{}, err
	}
	args := []string{
		"-p", p.Prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--mcp-config", mcpPath,
		"--strict-mcp-config",
		"--setting-sources", "user",
		"--settings", settingsPath,
		"--allowedTools", "mcp__platform__*",
	}
	if p.PackDir != "" {
		args = append(args, "--add-dir", p.PackDir)
		if _, err := os.Stat(filepath.Join(p.PackDir, ".claude-plugin", "plugin.json")); err == nil {
			args = append(args, "--plugin-dir", p.PackDir)
		}
	}
	if p.ReadOnly {
		args = append(args, "--permission-mode", "plan")
	} else {
		args = append(args, "--permission-mode", "acceptEdits")
	}
	if p.ResumeSessionID != "" {
		args = append(args, "--resume", p.ResumeSessionID)
	}
	env := allowedEnvironment()
	if p.HardenedRuntime {
		env = append(env, "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1")
	}
	return supervisor.Spec{
		Name:    "claude-" + p.Job.AttemptID,
		Command: bin,
		Args:    args,
		Dir:     p.WorkDir,
		Env:     env,
	}, nil
}

func allowedEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TMPDIR": true, "LANG": true, "LC_ALL": true, "TERM": true,
		"DOCKER_HOST": true, "XDG_RUNTIME_DIR": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
	}
	out := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}

// ContainerEnvironment returns only runner-generated provider controls that
// must cross the Docker CLI boundary. Host credentials, proxy settings, and
// arbitrary environment variables are never forwarded by this function.
func ContainerEnvironment(env []string) []string {
	allowed := map[string]bool{
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": true,
		"OPENCODE_CONFIG_CONTENT":          true,
		"OPENCODE_CONFIG_DIR":              true,
		"OPENCODE_DISABLE_AUTOUPDATE":      true,
		"OPENCODE_DISABLE_SHARE":           true,
		"OPENCODE_DISABLE_LSP_DOWNLOAD":    true,
		"OPENCODE_DISABLE_DEFAULT_PLUGINS": true,
		"OPENCODE_DISABLE_PROJECT_CONFIG":  true,
	}
	out := make([]string, 0, len(allowed))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}

func (c *Claude) ParseLine(line []byte) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, false
	}
	return m, true
}

func For(name string) (Adapter, error) {
	switch name {
	case "codex":
		return NewCodex(), nil
	case "claude":
		return NewClaude(), nil
	case "opencode":
		return NewOpenCode(), nil
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}
