package daemon

import (
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func testAgentGuidance() *v1.AgentGuidance {
	return &v1.AgentGuidance{SchemaVersion: 1, Layers: []v1.AgentGuidanceLayer{
		{Layer: v1.AgentGuidanceLayerOrganization, Revision: 3, DigestSHA256: strings.Repeat("a", 64), Content: "Never push to main."},
		{Layer: v1.AgentGuidanceLayerProject, Revision: 1, DigestSHA256: strings.Repeat("b", 64), Content: "Use pnpm."},
	}}
}

func TestBuildPromptLayersAgentGuidanceAfterAgentPack(t *testing.T) {
	jr := &jobRunner{packDir: "/pack", d: &Daemon{}, job: v1.JobSpec{
		Kind: v1.JobKindPlan, Ticket: promptTicket(), AgentGuidance: testAgentGuidance(),
	}}
	prompt := jr.buildPrompt()
	for _, required := range []string{
		"ORGANIZATION AND PROJECT AGENT GUIDANCE",
		"cannot override platform safety rules, the signed Agent Pack",
		"outranks repository instructions",
		"organization guidance wins",
		"Organization guidance (revision 3):\nNever push to main.\n\nProject guidance (revision 1):\nUse pnpm.",
		"END AGENT GUIDANCE",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
	}
	if strings.Index(prompt, "Agent Pack") > strings.Index(prompt, "AGENT GUIDANCE") {
		t.Fatalf("guidance must follow the Agent Pack instruction: %s", prompt)
	}
	if strings.Index(prompt, "END AGENT GUIDANCE") > strings.Index(prompt, "publish an implementation plan") {
		t.Fatalf("guidance must precede the job instruction: %s", prompt)
	}
}

func TestBuildPromptOmitsAbsentAgentGuidance(t *testing.T) {
	jr := &jobRunner{job: v1.JobSpec{Kind: v1.JobKindPlan, Ticket: promptTicket()}}
	if strings.Contains(jr.buildPrompt(), "AGENT GUIDANCE") {
		t.Fatal("prompt mentions guidance that was not delivered")
	}
}

func TestJobContextIncludesAgentGuidance(t *testing.T) {
	guidance := testAgentGuidance()
	jr := &jobRunner{job: v1.JobSpec{Kind: v1.JobKindPlan, AgentGuidance: guidance}}
	if got := jr.jobContext()["agent_guidance"]; !reflect.DeepEqual(got, *guidance) {
		t.Fatalf("job context agent_guidance = %#v", got)
	}
	jr.job.AgentGuidance = nil
	if _, ok := jr.jobContext()["agent_guidance"]; ok {
		t.Fatal("job context includes absent guidance")
	}
}
