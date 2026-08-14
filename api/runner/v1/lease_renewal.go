package v1

import "time"

type LeaseRenewalEvent struct {
	Kind           string    `json:"kind"`
	ManifestKind   JobKind   `json:"manifest_kind"`
	LeaseID        string    `json:"lease_id"`
	JobID          string    `json:"job_id"`
	AttemptID      string    `json:"attempt_id"`
	ManifestID     string    `json:"manifest_id"`
	ManifestDigest string    `json:"manifest_digest"`
	FencingToken   uint64    `json:"fencing_token"`
	ObservedAt     time.Time `json:"observed_at"`
}
