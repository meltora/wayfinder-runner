package v1

import "time"

const ProtocolVersion = 1

type MessageType string

const (
	TypeHello         MessageType = "hello"
	TypeHelloAck      MessageType = "hello_ack"
	TypeCapability    MessageType = "capability_manifest"
	TypeHeartbeat     MessageType = "heartbeat"
	TypeHeartbeatAck  MessageType = "heartbeat_ack"
	TypeCommand       MessageType = "command"
	TypeCommandAck    MessageType = "command_ack"
	TypeEvent         MessageType = "event"
	TypeEventAck      MessageType = "event_ack"
	TypePause         MessageType = "pause"
	TypeEmergencyStop MessageType = "emergency_stop"
	TypeError         MessageType = "error"
)

type Envelope struct {
	Version   int         `json:"version"`
	Type      MessageType `json:"type"`
	ID        string      `json:"id"`
	SentAt    time.Time   `json:"sent_at"`
	RunnerID  string      `json:"runner_id,omitempty"`
	InReplyTo string      `json:"in_reply_to,omitempty"`
	Payload   any         `json:"payload,omitempty"`

	ProviderCredential *ProviderCredential `json:"provider_credential,omitempty"`
}

type HelloPayload struct {
	ProtocolVersion int    `json:"protocol_version"`
	RunnerVersion   string `json:"runner_version"`
}

type HelloAckPayload struct {
	Accepted          bool   `json:"accepted"`
	RejectReason      string `json:"reject_reason,omitempty"`
	HeartbeatInterval int    `json:"heartbeat_interval_seconds"`
}

type HeartbeatPayload struct {
	State        string `json:"state"`
	ActiveJobs   int    `json:"active_jobs"`
	UnackedCount int    `json:"unacked_count"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
