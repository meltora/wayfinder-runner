package agentpack

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const (
	GatewayArchivePathPrefix = "/v1/agent-packs/sha256/"
	publishPackNameMaxBytes  = 256
	publishURLMaxBytes       = 4096
)

var publishURLPattern = regexp.MustCompile(`^https://((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)*[a-z](?:[a-z0-9-]*[a-z0-9])?)(?::([1-9][0-9]{0,4}))?(/(?:[A-Za-z0-9._~!$&'()*+,;=:@/-]|%[0-9A-Fa-f]{2})*)?$`)

func validatePublishPackName(name string) error {
	if len(name) > publishPackNameMaxBytes || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || validation.IsECMAScriptBlank(name) {
		return fmt.Errorf("pack name must be 1-%d bytes of valid UTF-8 without NUL and contain visible text", publishPackNameMaxBytes)
	}
	return nil
}

func validatePublishURL(raw string) error {
	match := publishURLPattern.FindStringSubmatch(raw)
	if len(raw) > publishURLMaxBytes || match == nil {
		return fmt.Errorf("agent pack URL must be https://<lowercase host>[:port][/path] without query, fragment or credentials")
	}
	for _, label := range strings.Split(match[1], ".") {
		if strings.HasPrefix(label, "xn--") || len(label) > 63 {
			return fmt.Errorf("agent pack URL host must use plain ASCII labels of at most 63 bytes")
		}
	}
	if match[2] != "" {
		if port, err := strconv.Atoi(match[2]); err != nil || port > 65535 {
			return fmt.Errorf("agent pack URL port is out of range")
		}
	}
	return nil
}

type PublicationRequest struct {
	ScopeKind            string
	OrganizationID       string
	PackKey              string
	PackName             string
	Provider             string
	ProtocolVersion      int
	MinimumRunnerVersion string
	KeyID                string
	URL                  string
	RequestID            string
	Archive              *CanonicalArchive
}

type PublishScope struct {
	Kind           string `json:"kind"`
	OrganizationID string `json:"organizationId,omitempty"`
}

type PublishRequest struct {
	Scope                PublishScope    `json:"scope"`
	PackKey              string          `json:"packKey"`
	PackName             string          `json:"packName"`
	RequestID            string          `json:"requestId"`
	Provider             string          `json:"provider"`
	ProtocolVersion      int             `json:"protocolVersion"`
	MinimumRunnerVersion string          `json:"minimumRunnerVersion"`
	SignatureBase64      string          `json:"signatureBase64"`
	AgentPack            v1.AgentPackRef `json:"agentPack"`
}

type Signer interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
	PublicKeySPKI() ([]byte, error)
}

type PublicKeyIdentity struct {
	PublicKeySPKIBase64 string `json:"publicKeySpkiBase64"`
	SPKISHA256          string `json:"spkiSha256"`
}

func SignerPublicKeyIdentity(signer Signer) (PublicKeyIdentity, error) {
	if signer == nil {
		return PublicKeyIdentity{}, fmt.Errorf("Agent Pack signer is required")
	}
	der, err := signer.PublicKeySPKI()
	if err != nil {
		return PublicKeyIdentity{}, err
	}
	if _, err := ed25519PublicKeyFromSPKIBase64(base64.StdEncoding.EncodeToString(der)); err != nil {
		return PublicKeyIdentity{}, fmt.Errorf("Agent Pack signer public key is invalid: %w", err)
	}
	sum := sha256.Sum256(der)
	return PublicKeyIdentity{PublicKeySPKIBase64: base64.StdEncoding.EncodeToString(der), SPKISHA256: hex.EncodeToString(sum[:])}, nil
}

func SignPublicationAttestation(ctx context.Context, signer Signer, authority v1.SignedAgentPackAuthority) (v1.SignedAgentPackAuthority, error) {
	if signer == nil {
		return authority, fmt.Errorf("Agent Pack signer is required")
	}
	digest, err := PublicationAttestationDigest(authority)
	if err != nil {
		return authority, err
	}
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != sha256.Size {
		return authority, fmt.Errorf("Agent Pack publication attestation digest is invalid")
	}
	signature, err := signer.Sign(ctx, raw)
	if err != nil {
		return authority, err
	}
	authority.AttestationDigest = digest
	authority.SignatureBase64 = base64.StdEncoding.EncodeToString(signature)
	return authority, nil
}

func GatewayArchiveURL(origin, digest string) (string, error) {
	if !lowerHexSHA256(digest) {
		return "", fmt.Errorf("Agent Pack digest is invalid")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("gateway origin must be an https origin without path, query, credentials or fragment")
	}
	return "https://" + parsed.Host + GatewayArchivePathPrefix + digest, nil
}

func SignPublishRequest(ctx context.Context, signer Signer, request PublicationRequest) (PublishRequest, error) {
	if request.Archive == nil {
		return PublishRequest{}, fmt.Errorf("a validated Agent Pack archive is required")
	}
	manifest := request.Archive.Manifest
	if request.PackKey != manifest.PackKey {
		return PublishRequest{}, fmt.Errorf("pack key %q does not match the archive manifest pack key %q", request.PackKey, manifest.PackKey)
	}
	if request.Provider != manifest.Provider {
		return PublishRequest{}, fmt.Errorf("provider %q does not match the archive manifest provider %q", request.Provider, manifest.Provider)
	}
	if err := validation.ID("requestId", request.RequestID); err != nil {
		return PublishRequest{}, err
	}
	if err := validatePublishPackName(request.PackName); err != nil {
		return PublishRequest{}, err
	}
	if err := validatePublishURL(request.URL); err != nil {
		return PublishRequest{}, err
	}
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: v1.AgentPackAuthoritySchemaVersion, RegistryVersionID: "unpublished",
		Scope:   v1.AgentPackAuthorityScope{Kind: request.ScopeKind, OrganizationID: request.OrganizationID},
		PackKey: request.PackKey, PackName: request.PackName, Provider: request.Provider,
		ProtocolVersion: request.ProtocolVersion, MinimumRunnerVersion: request.MinimumRunnerVersion,
		AgentPack: v1.AgentPackRef{Version: manifest.Version, Digest: request.Archive.Digest, URL: request.URL, KeyID: request.KeyID},
	}
	signed, err := SignPublicationAttestation(ctx, signer, authority)
	if err != nil {
		return PublishRequest{}, err
	}
	if err := validation.SignedAgentPackAuthority(signed); err != nil {
		return PublishRequest{}, err
	}
	return PublishRequest{
		Scope:   PublishScope{Kind: signed.Scope.Kind, OrganizationID: signed.Scope.OrganizationID},
		PackKey: signed.PackKey, PackName: signed.PackName, RequestID: request.RequestID, Provider: signed.Provider,
		ProtocolVersion: signed.ProtocolVersion, MinimumRunnerVersion: signed.MinimumRunnerVersion,
		SignatureBase64: signed.SignatureBase64, AgentPack: signed.AgentPack,
	}, nil
}
