package isolation

import "github.com/example/app-runner/internal/safeio"

const (
	AgentGitName  = "our-runner agent"
	AgentGitEmail = "agent@localhost"
)

const agentGitConfig = "[user]\n" +
	"\tname = " + AgentGitName + "\n" +
	"\temail = " + AgentGitEmail + "\n" +
	"[branch]\n" +
	"\tautoSetupMerge = false\n"

func WriteAgentGitConfig(home string) error {
	return safeio.AtomicWriteFile(home, ".gitconfig", []byte(agentGitConfig), 0o600)
}
