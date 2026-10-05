package agentpack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	SigningKeyProofPurpose     = "shortstack.agent-pack.signing-key-registration.v1"
	SigningKeyProofMaxBytes    = 4096
	signingKeyProofInstant     = "2006-01-02T15:04:05.000Z"
	signingKeyProofScopeOrg    = "ORGANIZATION"
	signingKeyProofScopeGlobal = "PLATFORM"
)

var (
	signingKeyProofKeys = []string{
		"actorUserId", "challengeId", "expiresAt", "keyId", "nonce", "notAfter", "notBefore",
		"organizationId", "purpose", "scopeType", "spkiSha256",
	}
	signingKeyProofKeyID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	signingKeyProofOpaqueID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	signingKeyProofChallenge = regexp.MustCompile(`^privileged_reauth_[0-9a-f]{64}$`)
	signingKeyProofHex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	signingKeyProofInstantRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
)

type SigningKeyProofMessage struct {
	ActorUserID    string
	ChallengeID    string
	ExpiresAt      string
	KeyID          string
	Nonce          string
	NotAfter       string
	NotBefore      string
	OrganizationID *string
	Purpose        string
	ScopeType      string
	SPKISHA256     string
}

func (message SigningKeyProofMessage) CanonicalJSON() []byte {
	organization := "null"
	if message.OrganizationID != nil {
		organization = `"` + *message.OrganizationID + `"`
	}
	var out bytes.Buffer
	out.WriteString(`{"actorUserId":"` + message.ActorUserID + `"`)
	out.WriteString(`,"challengeId":"` + message.ChallengeID + `"`)
	out.WriteString(`,"expiresAt":"` + message.ExpiresAt + `"`)
	out.WriteString(`,"keyId":"` + message.KeyID + `"`)
	out.WriteString(`,"nonce":"` + message.Nonce + `"`)
	out.WriteString(`,"notAfter":"` + message.NotAfter + `"`)
	out.WriteString(`,"notBefore":"` + message.NotBefore + `"`)
	out.WriteString(`,"organizationId":` + organization)
	out.WriteString(`,"purpose":"` + message.Purpose + `"`)
	out.WriteString(`,"scopeType":"` + message.ScopeType + `"`)
	out.WriteString(`,"spkiSha256":"` + message.SPKISHA256 + `"}`)
	return out.Bytes()
}

func signingKeyProofError(reason string) error {
	return fmt.Errorf("proof message is not a canonical Agent Pack signing-key registration message: %s", reason)
}

func ParseSigningKeyProofMessage(raw []byte, signerSPKISHA256 string, now time.Time) (SigningKeyProofMessage, error) {
	if len(raw) == sha256.Size || len(raw) > SigningKeyProofMaxBytes || !utf8.Valid(raw) {
		return SigningKeyProofMessage{}, signingKeyProofError("unexpected size or encoding")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != len(signingKeyProofKeys) {
		return SigningKeyProofMessage{}, signingKeyProofError("unexpected structure")
	}
	values := make(map[string]string, len(signingKeyProofKeys))
	var organization *string
	for _, key := range signingKeyProofKeys {
		field, ok := fields[key]
		if !ok {
			return SigningKeyProofMessage{}, signingKeyProofError("unexpected keys")
		}
		if key == "organizationId" && string(field) == "null" {
			continue
		}
		var value string
		if err := json.Unmarshal(field, &value); err != nil {
			return SigningKeyProofMessage{}, signingKeyProofError(key + " must be a string")
		}
		values[key] = value
		if key == "organizationId" {
			organization = &value
		}
	}
	message := SigningKeyProofMessage{
		ActorUserID: values["actorUserId"], ChallengeID: values["challengeId"], ExpiresAt: values["expiresAt"],
		KeyID: values["keyId"], Nonce: values["nonce"], NotAfter: values["notAfter"], NotBefore: values["notBefore"],
		OrganizationID: organization, Purpose: values["purpose"], ScopeType: values["scopeType"], SPKISHA256: values["spkiSha256"],
	}
	if message.Purpose != SigningKeyProofPurpose {
		return SigningKeyProofMessage{}, signingKeyProofError("purpose is not " + SigningKeyProofPurpose)
	}
	if err := message.validate(now); err != nil {
		return SigningKeyProofMessage{}, err
	}
	if !bytes.Equal(message.CanonicalJSON(), raw) {
		return SigningKeyProofMessage{}, signingKeyProofError("bytes are not the canonical serialization")
	}
	if message.SPKISHA256 != signerSPKISHA256 {
		return SigningKeyProofMessage{}, signingKeyProofError("spkiSha256 does not match the signing key")
	}
	return message, nil
}

func (message SigningKeyProofMessage) validate(now time.Time) error {
	switch {
	case !signingKeyProofOpaqueID.MatchString(message.ActorUserID):
		return signingKeyProofError("actorUserId is invalid")
	case !signingKeyProofChallenge.MatchString(message.ChallengeID):
		return signingKeyProofError("challengeId is invalid")
	case !signingKeyProofKeyID.MatchString(message.KeyID):
		return signingKeyProofError("keyId is invalid")
	case !signingKeyProofHex.MatchString(message.Nonce):
		return signingKeyProofError("nonce is invalid")
	case !signingKeyProofHex.MatchString(message.SPKISHA256):
		return signingKeyProofError("spkiSha256 is invalid")
	}
	switch message.ScopeType {
	case signingKeyProofScopeGlobal:
		if message.OrganizationID != nil {
			return signingKeyProofError("a platform proof must not name an organization")
		}
	case signingKeyProofScopeOrg:
		if message.OrganizationID == nil || !signingKeyProofOpaqueID.MatchString(*message.OrganizationID) {
			return signingKeyProofError("an organization proof must name a valid organization")
		}
	default:
		return signingKeyProofError("scopeType is invalid")
	}
	expiresAt, okExpires := parseSigningKeyProofInstant(message.ExpiresAt)
	notBefore, okBefore := parseSigningKeyProofInstant(message.NotBefore)
	notAfter, okAfter := parseSigningKeyProofInstant(message.NotAfter)
	switch {
	case !okExpires || !okBefore || !okAfter:
		return signingKeyProofError("instants must be canonical UTC milliseconds")
	case !notBefore.Before(notAfter):
		return signingKeyProofError("notBefore must precede notAfter")
	case !now.Before(expiresAt):
		return signingKeyProofError("the challenge has expired")
	}
	return nil
}

func parseSigningKeyProofInstant(value string) (time.Time, bool) {
	if !signingKeyProofInstantRe.MatchString(value) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(signingKeyProofInstant, value)
	if err != nil || parsed.UTC().Format(signingKeyProofInstant) != value {
		return time.Time{}, false
	}
	return parsed, true
}

func SignSigningKeyProof(ctx context.Context, signer Signer, raw []byte, now time.Time) ([]byte, error) {
	identity, err := SignerPublicKeyIdentity(signer)
	if err != nil {
		return nil, err
	}
	if _, err := ParseSigningKeyProofMessage(raw, identity.SPKISHA256, now); err != nil {
		return nil, err
	}
	return signer.Sign(ctx, raw)
}
