package v1

import (
	"encoding/json"
	"testing"
)

func TestCommandAckPayloadCarriesOnlyControlledRejectionEvidence(t *testing.T) {
	t.Parallel()
	payload := CommandAckPayload{
		Status:     CommandAckRejected,
		ReasonCode: CommandAckReasonAgentPackAuthorityInvalid,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["status"] != "rejected" || fields["reason_code"] != "agent_pack_authority_invalid" {
		t.Fatalf("acknowledgement fields = %v", fields)
	}
	for _, forbidden := range []string{"error", "message", "detail", "url", "path"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("unsafe field %q present in %s", forbidden, raw)
		}
	}
}

func TestCommandAckEvidenceMatchesStatus(t *testing.T) {
	t.Parallel()
	valid := []CommandAckPayload{
		{Status: CommandAckAccepted},
		{Status: CommandAckDuplicate},
		{Status: CommandAckRejected, ReasonCode: CommandAckReasonRunnerPaused},
	}
	for _, payload := range valid {
		if err := ValidateCommandAckPayload(payload); err != nil {
			t.Fatalf("valid payload rejected: %#v: %v", payload, err)
		}
	}
	invalid := []CommandAckPayload{
		{Status: CommandAckRejected},
		{Status: CommandAckAccepted, ReasonCode: CommandAckReasonRunnerPaused},
		{Status: "unknown"},
		{Status: CommandAckRejected, ReasonCode: "raw error text"},
	}
	for _, payload := range invalid {
		if err := ValidateCommandAckPayload(payload); err == nil {
			t.Fatalf("invalid payload accepted: %#v", payload)
		}
	}
}

func TestCommandAckAcceptsTrustChainRejectionCodes(t *testing.T) {
	t.Parallel()
	for code, wire := range map[CommandAckReasonCode]string{
		CommandAckReasonAgentPackTrustUnverifiable: "agent_pack_trust_unverifiable",
		CommandAckReasonAgentPackKeyRevoked:        "agent_pack_key_revoked",
	} {
		if string(code) != wire {
			t.Fatalf("reason code %q != %q", code, wire)
		}
		if err := ValidateCommandAckPayload(CommandAckPayload{Status: CommandAckRejected, ReasonCode: code}); err != nil {
			t.Fatalf("%s rejected: %v", code, err)
		}
	}
}
