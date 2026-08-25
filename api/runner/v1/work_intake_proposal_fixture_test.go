package v1_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type workIntakeProposalCommandFixture struct {
	Version  int               `json:"version"`
	Type     v1.MessageType    `json:"type"`
	ID       string            `json:"id"`
	SentAt   time.Time         `json:"sent_at"`
	RunnerID string            `json:"runner_id"`
	Payload  v1.CommandPayload `json:"payload"`
}

type workIntakeProposalResultFixture struct {
	Version  int                              `json:"version"`
	Type     v1.MessageType                   `json:"type"`
	ID       string                           `json:"id"`
	SentAt   time.Time                        `json:"sent_at"`
	RunnerID string                           `json:"runner_id"`
	Payload  v1.WorkIntakeProposalResultEvent `json:"payload"`
}

func TestCanonicalWorkIntakeProposalFixturesStrictlyParseAndValidate(t *testing.T) {
	var command workIntakeProposalCommandFixture
	decodeStrictFixture(t, "testdata/work_intake_proposal_v1_command.json", &command)
	if command.Version != v1.ProtocolVersion || command.Type != v1.TypeCommand ||
		command.Payload.Command != v1.CmdWorkIntakeProposal || command.Payload.WorkIntakeProposal == nil {
		t.Fatalf("command fixture = %+v", command)
	}
	if err := validation.WorkIntakeProposalOperation(*command.Payload.WorkIntakeProposal); err != nil {
		t.Fatal(err)
	}

	var result workIntakeProposalResultFixture
	decodeStrictFixture(t, "testdata/work_intake_proposal_v1_result.json", &result)
	if result.Version != v1.ProtocolVersion || result.Type != v1.TypeEvent {
		t.Fatalf("result fixture = %+v", result)
	}
	if err := validation.WorkIntakeProposalResultEvent(result.Payload); err != nil {
		t.Fatal(err)
	}
	if result.Payload.WorkIntakeProposalAuthority != command.Payload.WorkIntakeProposal.WorkIntakeProposalAuthority {
		t.Fatal("result does not echo the exact command authority")
	}
}

func decodeStrictFixture(t *testing.T, path string, destination any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatal(err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatal("fixture contains trailing JSON")
	}
}
