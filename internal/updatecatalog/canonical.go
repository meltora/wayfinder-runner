package updatecatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	maxEnvelopeBytes = 256 << 10
	maxPayloadBytes  = 192 << 10
)

func CanonicalPayload(manifest Manifest) ([]byte, error) {
	normalized, err := normalizedManifest(manifest)
	if err != nil {
		return nil, err
	}
	wire := toWire(normalized)
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, invalid("encode payload: %v", err)
	}
	return payload, nil
}

// PayloadDigest is the deterministic SHA-256 identity used for durable replay
// and equivocation state. It intentionally differs from SigningDigest.
func PayloadDigest(manifest Manifest) ([32]byte, error) {
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

// ParsePayload accepts only the exact canonical JSON emitted by
// CanonicalPayload. It is intended for release tooling that transports the
// payload between a generator, an external signer, and Assemble.
func ParsePayload(payload []byte) (Manifest, error) {
	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return Manifest{}, malformed("payload size is invalid")
	}
	var wire manifestWire
	if err := strictDecode(payload, &wire); err != nil {
		return Manifest{}, malformed("decode payload: %v", err)
	}
	manifest, err := fromWire(wire)
	if err != nil {
		return Manifest{}, err
	}
	canonical, err := CanonicalPayload(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(payload, canonical) {
		return Manifest{}, malformed("payload is not canonical JSON")
	}
	return cloneManifest(manifest), nil
}

// SigningDigest is the exact digest external signers must sign with ECDSA
// P-256. The NUL separator prevents cross-protocol ambiguity.
func SigningDigest(manifest Manifest) ([32]byte, error) {
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		return [32]byte{}, err
	}
	message := make([]byte, 0, len(SignatureDomain)+1+len(payload))
	message = append(message, SignatureDomain...)
	message = append(message, 0)
	message = append(message, payload...)
	return sha256.Sum256(message), nil
}

func normalizedManifest(manifest Manifest) (Manifest, error) {
	copy := cloneManifest(manifest)
	if err := validateManifest(copy); err != nil {
		return Manifest{}, err
	}
	copy.IssuedAt = copy.IssuedAt.UTC()
	copy.ExpiresAt = copy.ExpiresAt.UTC()
	sort.Slice(copy.Artifacts, func(i, j int) bool {
		if copy.Artifacts[i].OS != copy.Artifacts[j].OS {
			return copy.Artifacts[i].OS < copy.Artifacts[j].OS
		}
		return copy.Artifacts[i].Arch < copy.Artifacts[j].Arch
	})
	return copy, nil
}

func toWire(manifest Manifest) manifestWire {
	artifacts := make([]artifactWire, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts = append(artifacts, artifactWire{
			OS: artifact.OS, Arch: artifact.Arch, Format: artifact.Format,
			URL: artifact.URL, SHA256: artifact.SHA256, Size: artifact.Size,
		})
	}
	return manifestWire{
		Schema: manifest.Schema, Channel: manifest.Channel, Sequence: manifest.Sequence,
		ReleaseID: manifest.ReleaseID, Version: manifest.Version,
		IssuedAt: manifest.IssuedAt.Format(timestampLayout), ExpiresAt: manifest.ExpiresAt.Format(timestampLayout),
		MinRunner: manifest.MinRunner, MinUpdater: manifest.MinUpdater, Artifacts: artifacts,
	}
}

func fromWire(wire manifestWire) (Manifest, error) {
	issuedAt, err := parseWireTime("issued_at", wire.IssuedAt)
	if err != nil {
		return Manifest{}, err
	}
	expiresAt, err := parseWireTime("expires_at", wire.ExpiresAt)
	if err != nil {
		return Manifest{}, err
	}
	artifacts := make([]Artifact, 0, len(wire.Artifacts))
	for _, artifact := range wire.Artifacts {
		artifacts = append(artifacts, Artifact{
			OS: artifact.OS, Arch: artifact.Arch, Format: artifact.Format,
			URL: artifact.URL, SHA256: artifact.SHA256, Size: artifact.Size,
		})
	}
	return Manifest{
		Schema: wire.Schema, Channel: wire.Channel, Sequence: wire.Sequence,
		ReleaseID: wire.ReleaseID, Version: wire.Version, IssuedAt: issuedAt, ExpiresAt: expiresAt,
		MinRunner: wire.MinRunner, MinUpdater: wire.MinUpdater, Artifacts: artifacts,
	}, nil
}

func parseWireTime(field, value string) (time.Time, error) {
	if len(value) != len(timestampLayout) || !strings.HasSuffix(value, "Z") {
		return time.Time{}, invalid("%s must use RFC3339 whole-second UTC form", field)
	}
	parsed, err := time.Parse(timestampLayout, value)
	if err != nil || parsed.Format(timestampLayout) != value {
		return time.Time{}, invalid("%s must use RFC3339 whole-second UTC form", field)
	}
	return parsed, nil
}

func parseEnvelope(data []byte) (Manifest, []Signature, []byte, error) {
	if len(data) == 0 || len(data) > maxEnvelopeBytes {
		return Manifest{}, nil, nil, malformed("envelope size is invalid")
	}
	var envelope signedEnvelope
	if err := strictDecode(data, &envelope); err != nil {
		return Manifest{}, nil, nil, malformed("decode envelope: %v", err)
	}
	if envelope.Payload == "" || len(envelope.Signatures) == 0 || len(envelope.Signatures) > 32 {
		return Manifest{}, nil, nil, malformed("payload and 1-32 signatures are required")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil || base64.StdEncoding.EncodeToString(payload) != envelope.Payload {
		return Manifest{}, nil, nil, malformed("payload is not canonical base64")
	}
	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return Manifest{}, nil, nil, malformed("payload size is invalid")
	}
	manifest, err := ParsePayload(payload)
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	seen := make(map[string]struct{}, len(envelope.Signatures))
	previousKeyID := ""
	for _, signature := range envelope.Signatures {
		if !namePattern.MatchString(signature.KeyID) {
			return Manifest{}, nil, nil, malformed("invalid signature key_id %q", signature.KeyID)
		}
		if _, exists := seen[signature.KeyID]; exists {
			return Manifest{}, nil, nil, malformed("duplicate signature key_id %q", signature.KeyID)
		}
		if previousKeyID != "" && signature.KeyID < previousKeyID {
			return Manifest{}, nil, nil, malformed("signatures are not in canonical key_id order")
		}
		seen[signature.KeyID] = struct{}{}
		previousKeyID = signature.KeyID
		decoded, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
		if decodeErr != nil || base64.StdEncoding.EncodeToString(decoded) != signature.Value {
			return Manifest{}, nil, nil, malformed("signature for %q is not canonical base64", signature.KeyID)
		}
		_, s, parseErr := parseECDSASignature(decoded)
		if parseErr != nil {
			return Manifest{}, nil, nil, malformed("signature for %q is invalid: %v", signature.KeyID, parseErr)
		}
		if !isLowS(s) {
			return Manifest{}, nil, nil, malformed("signature for %q is not canonical low-S ECDSA", signature.KeyID)
		}
	}
	return manifest, append([]Signature(nil), envelope.Signatures...), payload, nil
}

func strictDecode(data []byte, destination any) error {
	if err := rejectDuplicateFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid object closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid array closing delimiter")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}
