package v1

import "fmt"

type CommandAckStatus string

const (
	CommandAckAccepted  CommandAckStatus = "accepted"
	CommandAckRejected  CommandAckStatus = "rejected"
	CommandAckDuplicate CommandAckStatus = "duplicate"
)

type CommandAckReasonCode string

const (
	CommandAckReasonInvalidCommand            CommandAckReasonCode = "invalid_command"
	CommandAckReasonUnsupportedCommand        CommandAckReasonCode = "unsupported_command"
	CommandAckReasonCommandConflict           CommandAckReasonCode = "command_conflict"
	CommandAckReasonRunnerPaused              CommandAckReasonCode = "runner_paused"
	CommandAckReasonRunnerBusy                CommandAckReasonCode = "runner_busy"
	CommandAckReasonRunnerDraining            CommandAckReasonCode = "runner_draining"
	CommandAckReasonLeaseRejected             CommandAckReasonCode = "lease_rejected"
	CommandAckReasonAgentPackKeyUntrusted     CommandAckReasonCode = "agent_pack_key_untrusted"
	CommandAckReasonAgentPackAuthorityInvalid CommandAckReasonCode = "agent_pack_authority_invalid"
	CommandAckReasonAgentPackIncompatible     CommandAckReasonCode = "agent_pack_incompatible"
	CommandAckReasonAgentPackOriginUnapproved CommandAckReasonCode = "agent_pack_origin_unapproved"
	CommandAckReasonInternalRejection         CommandAckReasonCode = "internal_rejection"
)

var commandAckReasonCodes = map[CommandAckReasonCode]struct{}{
	CommandAckReasonInvalidCommand:            {},
	CommandAckReasonUnsupportedCommand:        {},
	CommandAckReasonCommandConflict:           {},
	CommandAckReasonRunnerPaused:              {},
	CommandAckReasonRunnerBusy:                {},
	CommandAckReasonRunnerDraining:            {},
	CommandAckReasonLeaseRejected:             {},
	CommandAckReasonAgentPackKeyUntrusted:     {},
	CommandAckReasonAgentPackAuthorityInvalid: {},
	CommandAckReasonAgentPackIncompatible:     {},
	CommandAckReasonAgentPackOriginUnapproved: {},
	CommandAckReasonInternalRejection:         {},
}

type CommandAckPayload struct {
	Status     CommandAckStatus     `json:"status"`
	ReasonCode CommandAckReasonCode `json:"reason_code,omitempty"`
}

func IsCommandAckReasonCode(value CommandAckReasonCode) bool {
	_, ok := commandAckReasonCodes[value]
	return ok
}

func ValidateCommandAckPayload(payload CommandAckPayload) error {
	switch payload.Status {
	case CommandAckAccepted, CommandAckDuplicate:
		if payload.ReasonCode != "" {
			return fmt.Errorf("successful command acknowledgement cannot contain a rejection reason")
		}
		return nil
	case CommandAckRejected:
		if !IsCommandAckReasonCode(payload.ReasonCode) {
			return fmt.Errorf("rejected command acknowledgement requires a controlled reason code")
		}
		return nil
	default:
		return fmt.Errorf("command acknowledgement status is invalid")
	}
}
