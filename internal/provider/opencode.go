package provider

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
)

var (
	openCodeModelPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]*/[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)
	openCodeSessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
)

// OpenCode is a provider-neutral coding harness adapter. Model selection is
// optional: when Model is empty, OpenCode uses the customer's configured
// default provider and model.
type OpenCode struct {
	LookPath func(string) (string, error)
}

func NewOpenCode() *OpenCode { return &OpenCode{} }

func (o *OpenCode) Name() string { return "opencode" }

func (o *OpenCode) Available() bool {
	if o.LookPath != nil {
		_, err := o.LookPath("opencode")
		return err == nil
	}
	_, err := exec_LookPath("opencode")
	return err == nil
}

func (o *OpenCode) BuildSpec(p StartParams) (supervisor.Spec, error) {
	if p.ConfigDir == "" || !filepath.IsAbs(p.ConfigDir) {
		return supervisor.Spec{}, fmt.Errorf("OpenCode job config requires a runner-owned absolute directory")
	}
	if !openCodeModelIsValid(p.Model) {
		return supervisor.Spec{}, fmt.Errorf("OpenCode model must use provider/model format")
	}
	if p.ResumeSessionID != "" && !openCodeSessionPattern.MatchString(p.ResumeSessionID) {
		return supervisor.Spec{}, fmt.Errorf("OpenCode session ID is invalid")
	}

	config, err := openCodeRuntimeConfig(p)
	if err != nil {
		return supervisor.Spec{}, err
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("encode OpenCode runtime config: %w", err)
	}

	bin := p.BinaryPath
	if bin == "" {
		bin = "opencode"
	}
	agent := "build"
	if p.ReadOnly {
		agent = "plan"
	}
	args := []string{"run", "--format", "json", "--auto", "--pure", "--agent", agent}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	// Production job homes are ephemeral and intentionally do not retain the
	// OpenCode session database. Direct development runs can resume the host's
	// local session; hardened jobs start from the complete control-plane prompt.
	if p.ResumeSessionID != "" && !p.HardenedRuntime {
		args = append(args, "--session", p.ResumeSessionID, "--fork")
	}
	args = append(args, p.Prompt)

	env := append(allowedEnvironment(),
		"OPENCODE_CONFIG_CONTENT="+string(configJSON),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_SHARE=1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
	)
	if p.PackDir != "" {
		env = append(env, "OPENCODE_CONFIG_DIR="+p.PackDir)
	}

	return supervisor.Spec{
		Name:    "opencode-" + p.Job.AttemptID,
		Command: bin,
		Args:    args,
		Dir:     p.WorkDir,
		Env:     env,
	}, nil
}

func openCodeRuntimeConfig(p StartParams) (map[string]any, error) {
	permission := map[string]any{
		"*":                  "deny",
		"read":               "allow",
		"glob":               "allow",
		"grep":               "allow",
		"edit":               "allow",
		"skill":              "allow",
		"platform_*":         "allow",
		"bash":               "deny",
		"lsp":                "deny",
		"task":               "deny",
		"question":           "deny",
		"webfetch":           "deny",
		"websearch":          "deny",
		"external_directory": "deny",
		"doom_loop":          "deny",
	}
	if p.ReadOnly {
		permission["edit"] = "deny"
	}
	command := []string{p.mcpBin(), "mcp-serve", "--socket", p.MCPSock, "--token-file", p.MCPTokenFile}
	config := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"plugin":  []string{},
		"mcp": map[string]any{
			"platform": map[string]any{
				"type":    "local",
				"command": command,
				"enabled": true,
			},
		},
		"permission": permission,
		"agent": map[string]any{
			"build": map[string]any{"permission": permission},
			"plan":  map[string]any{"permission": permission},
		},
	}
	if instructions := openCodeInstructionPaths(p); len(instructions) > 0 {
		config["instructions"] = instructions
	}
	return config, nil
}

func openCodeInstructionPaths(p StartParams) []string {
	paths := make([]string, 0, 4)
	for _, root := range []string{p.WorkDir, p.PackDir} {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			path := filepath.Join(root, name)
			if _, err := safeio.ReadRegularFile(path, 1<<20, 0); err == nil {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func (o *OpenCode) ParseLine(line []byte) (map[string]any, bool) {
	var event map[string]any
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, false
	}
	return event, true
}

func openCodeModelIsValid(model string) bool {
	return model == "" || (len(model) <= 256 && !strings.HasPrefix(model, "-") && openCodeModelPattern.MatchString(model))
}
