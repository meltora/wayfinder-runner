package isolation

import (
	"strings"

	"github.com/example/app-runner/internal/safeio"
)

const (
	AgentGitName  = "our-runner agent"
	AgentGitEmail = "agent@localhost"
)

const agentGitExcludesName = ".agent-git-excludes"

var claudeSandboxPlaceholderNames = []string{
	".bash_profile", ".bashrc", ".claude/", ".gitconfig", ".gitmodules", ".idea", ".mcp.json",
	".profile", ".ripgreprc", ".vscode", ".zprofile", ".zshrc",
}

const agentGitConfig = "[user]\n" +
	"\tname = " + AgentGitName + "\n" +
	"\temail = " + AgentGitEmail + "\n" +
	"[branch]\n" +
	"\tautoSetupMerge = false\n" +
	"[core]\n" +
	"\texcludesFile = " + ContainerHome + "/" + agentGitExcludesName + "\n"

func agentGitExcludes() []byte {
	var excludes strings.Builder
	for _, name := range claudeSandboxPlaceholderNames {
		excludes.WriteString("/" + name + "\n")
	}
	return []byte(excludes.String())
}

func WriteAgentGitConfig(home string) error {
	if err := safeio.AtomicWriteFile(home, agentGitExcludesName, agentGitExcludes(), 0o600); err != nil {
		return err
	}
	return safeio.AtomicWriteFile(home, ".gitconfig", []byte(agentGitConfig), 0o600)
}
