package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/mcpbridge"
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
	DockerIsolation bool
	Environment     []string
}

// PromptOnlyParams is intentionally disjoint from StartParams: consultation
// execution has no job, repository, pack, MCP, session, or tool authority.
type PromptOnlyParams struct {
	ExecutionID string
	WorkDir     string
	ConfigDir   string
	Prompt      string
	SchemaPath  string
	OutputPath  string
	BinaryPath  string
	Model       string
}

func validatePromptOnlyParams(p PromptOnlyParams) error {
	if p.ExecutionID == "" || p.Model == "" || strings.TrimSpace(p.Model) != p.Model || p.Prompt == "" {
		return fmt.Errorf("prompt-only execution requires an ID, canonical model, and prompt")
	}
	for name, path := range map[string]string{
		"workspace": p.WorkDir, "config": p.ConfigDir, "schema": p.SchemaPath, "output": p.OutputPath,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("prompt-only %s path must be absolute", name)
		}
	}
	for name, path := range map[string]string{"schema": p.SchemaPath, "output": p.OutputPath} {
		relative, err := filepath.Rel(p.ConfigDir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("prompt-only %s path must be inside the private config directory", name)
		}
	}
	return nil
}

func (p StartParams) providerEnvironment() []string {
	if len(p.Environment) == 0 {
		return nil
	}
	return append([]string(nil), p.Environment...)
}

func environmentValue(environment []string, name string) string {
	value := ""
	for _, entry := range environment {
		if key, candidate, ok := strings.Cut(entry, "="); ok && key == name {
			value = candidate
		}
	}
	return value
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
	ManagedHooks() ManagedHookCapability
	BuildSpec(p StartParams) (supervisor.Spec, error)
	ParseLine(line []byte) (map[string]any, bool)
}

func mcpServerEntry(socketPath, tokenFile, bin string) map[string]any {
	return map[string]any{
		"command": bin,
		"args":    []string{"mcp-serve", "--socket", socketPath, "--token-file", tokenFile},
		"timeout": (mcpbridge.ToolCallTimeout(mcpbridge.HumanInputTool) + time.Minute).Milliseconds(),
	}
}

type Codex struct {
	LookPath func(string) (string, error)
}

func NewCodex() *Codex { return &Codex{} }

func (c *Codex) Name() string { return "codex" }
func (c *Codex) ManagedHooks() ManagedHookCapability {
	return managedHookCapability("codex-command-hooks/v1", codexManagedHookDefinitions)
}
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
	managedConfigMounts, err := writeCodexManagedHooks(p)
	if err != nil {
		return supervisor.Spec{}, err
	}
	return supervisor.Spec{
		Name:                "codex-" + p.Job.AttemptID,
		Command:             bin,
		Args:                args,
		Dir:                 p.WorkDir,
		Env:                 allowedEnvironment(),
		ProviderEnvironment: p.providerEnvironment(),
		ManagedConfigMounts: managedConfigMounts,
	}, nil
}

func (c *Codex) BuildPromptOnlySpec(p PromptOnlyParams) (supervisor.Spec, error) {
	if err := validatePromptOnlyParams(p); err != nil {
		return supervisor.Spec{}, err
	}
	bin := p.BinaryPath
	if bin == "" {
		bin = "codex"
	}
	args := []string{
		"exec", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "-c", `approval_policy="never"`,
	}
	for _, feature := range []string{
		"shell_tool", "unified_exec", "code_mode_host", "apps", "browser_use", "browser_use_external",
		"computer_use", "image_generation", "standalone_web_search", "web_search_request",
	} {
		args = append(args, "--disable", feature)
	}
	args = append(args,
		"--model", p.Model, "--output-schema", p.SchemaPath,
		"--output-last-message", p.OutputPath, "--color", "never", "-",
	)
	return supervisor.Spec{
		Name: "codex-work-intake-proposal-" + p.ExecutionID, Command: bin,
		Args: args, Dir: p.WorkDir, Env: allowedEnvironment(), Stdin: strings.NewReader(p.Prompt),
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
func (c *Claude) ManagedHooks() ManagedHookCapability {
	return managedHookCapability("claude-command-hooks/v1", claudeManagedHookDefinitions)
}
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
	deniedPermissions := []string{
		"Read(~/.claude/**)", "Edit(~/.claude/**)",
		"Read(~/.claude.json)", "Edit(~/.claude.json)",
	}
	deniedPaths := []string{"~/.claude", "~/.claude.json"}
	deniedCredentialFiles := []map[string]string{
		{"path": "~/.claude/.credentials.json", "mode": "deny"},
		{"path": "~/.claude.json", "mode": "deny"},
	}
	if configDir := environmentValue(p.Environment, "CLAUDE_CONFIG_DIR"); filepath.IsAbs(configDir) {
		root := filepath.Clean(configDir)
		deniedPermissions = append(deniedPermissions, "Read(/"+root+"/**)", "Edit(/"+root+"/**)")
		deniedPaths = append(deniedPaths, root)
		deniedCredentialFiles = append(deniedCredentialFiles,
			map[string]string{"path": filepath.Join(root, ".credentials.json"), "mode": "deny"},
			map[string]string{"path": filepath.Join(root, ".claude.json"), "mode": "deny"},
		)
	}
	if p.ReadOnly {
		deniedPermissions = append(deniedPermissions, claudePlanDeniedTools...)
	}
	sandbox := map[string]any{
		"enabled":                  p.HardenedRuntime,
		"failIfUnavailable":        p.HardenedRuntime,
		"allowUnsandboxedCommands": false,
		"filesystem": map[string]any{
			"denyRead":  deniedPaths,
			"denyWrite": deniedPaths,
		},
		"credentials": map[string]any{
			"files": deniedCredentialFiles,
		},
		"network": map[string]any{"strictAllowlist": true, "allowedDomains": []string{}},
	}
	if p.DockerIsolation {
		sandbox["enableWeakerNestedSandbox"] = true
	}
	settings := map[string]any{
		"permissions": map[string]any{
			"deny": deniedPermissions,
		},
		"sandbox": sandbox,
	}
	hooks, err := claudeManagedHooks(p)
	if err != nil {
		return supervisor.Spec{}, err
	}
	if p.ReadOnly {
		if hooks, err = withPlanCommandGuard(p, hooks); err != nil {
			return supervisor.Spec{}, err
		}
	}
	if len(hooks) > 0 {
		settings["hooks"] = hooks
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
	}
	args = append(args, claudeSettingSourceArgs(p.ReadOnly)...)
	args = append(args, "--settings", settingsPath)
	if p.ReadOnly {
		args = append(args, claudePlanToolArgs()...)
	} else {
		args = append(args, "--allowedTools", "mcp__platform__*", "Bash")
	}
	if p.PackDir != "" {
		args = append(args, "--add-dir", p.PackDir)
		if _, err := os.Stat(filepath.Join(p.PackDir, ".claude-plugin", "plugin.json")); err == nil {
			args = append(args, "--plugin-dir", p.PackDir)
		}
	}
	if p.ReadOnly {
		args = append(args, claudePlanPermissionArgs()...)
	} else {
		args = append(args, "--permission-mode", "acceptEdits")
	}
	if p.ResumeSessionID != "" {
		args = append(args, "--resume", p.ResumeSessionID)
	}
	env := allowedEnvironment()
	if p.HardenedRuntime && !p.DockerIsolation {
		env = append(env, "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1")
	}
	return supervisor.Spec{
		Name:                "claude-" + p.Job.AttemptID,
		Command:             bin,
		Args:                args,
		Dir:                 p.WorkDir,
		Env:                 env,
		ProviderEnvironment: p.providerEnvironment(),
	}, nil
}

func (c *Claude) BuildPromptOnlySpec(p PromptOnlyParams) (supervisor.Spec, error) {
	if err := validatePromptOnlyParams(p); err != nil {
		return supervisor.Spec{}, err
	}
	schema, err := safeio.ReadRegularFile(p.SchemaPath, 64<<10, 0)
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("read prompt-only output schema: %w", err)
	}
	bin := p.BinaryPath
	if bin == "" {
		bin = "claude"
	}
	emptyMCPPath := filepath.Join(p.ConfigDir, "claude-empty-mcp.json")
	if err := safeio.AtomicWriteFile(p.ConfigDir, "claude-empty-mcp.json", []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		return supervisor.Spec{}, fmt.Errorf("write prompt-only empty MCP config: %w", err)
	}
	args := []string{
		"-p", "--input-format", "text", "--output-format", "json", "--json-schema", string(schema),
		"--tools", "", "--bare", "--no-session-persistence", "--model", p.Model,
		"--mcp-config", emptyMCPPath, "--strict-mcp-config",
	}
	return supervisor.Spec{
		Name: "claude-work-intake-proposal-" + p.ExecutionID, Command: bin,
		Args: args, Dir: p.WorkDir, Env: allowedEnvironment(), Stdin: strings.NewReader(p.Prompt),
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
