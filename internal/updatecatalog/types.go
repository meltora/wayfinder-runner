// Package updatecatalog defines the authenticated release catalog consumed by
// the privileged runner updater. Its v2 envelope supports offline and KMS
// signers, threshold trust, replay protection, and exact platform binding.
package updatecatalog

import (
	"crypto/ecdsa"
	"errors"
	"time"
)

const (
	SchemaV2        = "our-runner-update-manifest/v2"
	SignatureDomain = "our-runner:update-manifest:v2"
	MaxArtifactSize = int64(1 << 30)
)

var (
	ErrMalformed              = errors.New("malformed update catalog")
	ErrInvalidManifest        = errors.New("invalid update manifest")
	ErrInsufficientSignatures = errors.New("insufficient trusted signatures")
	ErrReplay                 = errors.New("update manifest replay")
	ErrEquivocation           = errors.New("update manifest equivocation")
	ErrNoArtifact             = errors.New("no artifact for platform")
	ErrVersionGate            = errors.New("update version gate failed")
)

type Manifest struct {
	Schema     string
	Channel    string
	Sequence   uint64
	ReleaseID  string
	Version    string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	MinRunner  string
	MinUpdater string
	Artifacts  []Artifact
}

type Artifact struct {
	OS     string
	Arch   string
	Format string
	URL    string
	SHA256 string
	Size   int64
}

// Signature is the JSON representation of a detached signature. Value is
// canonical base64-encoded ASN.1 DER ECDSA data.
type Signature struct {
	KeyID string `json:"key_id"`
	Value string `json:"signature"`
}

// DetachedSignature is suitable for signatures returned by a KMS or other
// external signer. Assemble validates and canonicalizes it.
type DetachedSignature struct {
	KeyID string
	Value []byte
}

type Signer struct {
	KeyID      string
	PrivateKey *ecdsa.PrivateKey
}

type TrustedKey struct {
	KeyID     string
	PublicKey *ecdsa.PublicKey
	NotBefore time.Time
	NotAfter  time.Time
	RevokedAt *time.Time
}

type TrustPolicy struct {
	Threshold int
	Keys      []TrustedKey
}

// State is durable verified state. PayloadSHA256 must be persisted alongside
// Sequence so an equal sequence carrying different content is rejected.
type State struct {
	Sequence      uint64
	ReleaseID     string
	PayloadSHA256 string
}

type VerifyPolicy struct {
	Channel        string
	OS             string
	Arch           string
	RunnerVersion  string
	UpdaterVersion string
	Now            time.Time
	ClockSkew      time.Duration
	MaxLifetime    time.Duration
	Trust          TrustPolicy
	Current        State
}

type VerifiedRelease struct {
	Manifest      Manifest
	Artifact      Artifact
	PayloadSHA256 string
	ValidKeyIDs   []string
	AlreadySeen   bool
}

type signedEnvelope struct {
	Payload    string      `json:"payload"`
	Signatures []Signature `json:"signatures"`
}

type manifestWire struct {
	Schema     string         `json:"schema"`
	Channel    string         `json:"channel"`
	Sequence   uint64         `json:"sequence"`
	ReleaseID  string         `json:"release_id"`
	Version    string         `json:"version"`
	IssuedAt   string         `json:"issued_at"`
	ExpiresAt  string         `json:"expires_at"`
	MinRunner  string         `json:"min_runner"`
	MinUpdater string         `json:"min_updater"`
	Artifacts  []artifactWire `json:"artifacts"`
}

type artifactWire struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func cloneManifest(m Manifest) Manifest {
	out := m
	out.Artifacts = append([]Artifact(nil), m.Artifacts...)
	return out
}
