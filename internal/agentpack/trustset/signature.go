package trustset

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustroot"
)

const SignatureDomain = "agent-pack-trusted-key-set/v1\x00"

var rootKeyIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Signer interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
	PublicKeySPKI() ([]byte, error)
}

type Verified struct {
	Document       Document
	Signed         v1.AgentPackTrustedKeySet
	RootKeyID      string
	DocumentSHA256 string
}

func SigningMessage(document []byte) []byte {
	message := make([]byte, 0, len(SignatureDomain)+len(document))
	message = append(message, SignatureDomain...)
	return append(message, document...)
}

func Sign(ctx context.Context, signer Signer, document Document) (v1.AgentPackTrustedKeySet, error) {
	if signer == nil {
		return v1.AgentPackTrustedKeySet{}, fmt.Errorf("a root signing key is required")
	}
	canonical := document.Canonical()
	if _, err := ParseCanonical(canonical); err != nil {
		return v1.AgentPackTrustedKeySet{}, err
	}
	der, err := signer.PublicKeySPKI()
	if err != nil {
		return v1.AgentPackTrustedKeySet{}, err
	}
	encodedPublicKey := base64.StdEncoding.EncodeToString(der)
	publicKey, rootKeyID, err := trustroot.Ed25519FromSPKIBase64(encodedPublicKey)
	if err != nil {
		return v1.AgentPackTrustedKeySet{}, fmt.Errorf("root signing key: %w", err)
	}
	signature, err := signer.Sign(ctx, SigningMessage(canonical))
	if err != nil {
		return v1.AgentPackTrustedKeySet{}, err
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, SigningMessage(canonical), signature) {
		return v1.AgentPackTrustedKeySet{}, fmt.Errorf("root signing key produced an invalid signature")
	}
	return v1.AgentPackTrustedKeySet{
		DocumentBase64:  base64.StdEncoding.EncodeToString(canonical),
		SignatureBase64: base64.StdEncoding.EncodeToString(signature),
		RootKeyID:       rootKeyID,
	}, nil
}

func Verify(signed v1.AgentPackTrustedKeySet, roots trustroot.Roots) (Verified, error) {
	if !rootKeyIDPattern.MatchString(signed.RootKeyID) {
		return Verified{}, refuse(CodeInvalidEnvelope, "root_key_id must be a lowercase SHA-256 fingerprint")
	}
	if len(signed.DocumentBase64) > base64.StdEncoding.EncodedLen(MaxDocumentBytes) {
		return Verified{}, refuse(CodeTooLarge, "document exceeds %d bytes", MaxDocumentBytes)
	}
	document, err := canonicalBase64(signed.DocumentBase64)
	if err != nil || len(document) == 0 {
		return Verified{}, refuse(CodeInvalidEnvelope, "document_base64 must be canonical base64")
	}
	if len(document) > MaxDocumentBytes {
		return Verified{}, refuse(CodeTooLarge, "document exceeds %d bytes", MaxDocumentBytes)
	}
	signature, err := canonicalBase64(signed.SignatureBase64)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Verified{}, refuse(CodeInvalidEnvelope, "signature_base64 must encode a 64-byte Ed25519 signature")
	}
	root, ok := roots.Find(signed.RootKeyID)
	if !ok {
		return Verified{}, refuse(CodeUnknownRoot, "root %s is not embedded in this %s runner", signed.RootKeyID, roots.Channel)
	}
	if !ed25519.Verify(root.PublicKey, SigningMessage(document), signature) {
		return Verified{}, refuse(CodeSignatureInvalid, "signature does not verify against root %s", signed.RootKeyID)
	}
	parsed, err := ParseCanonical(document)
	if err != nil {
		return Verified{}, err
	}
	sum := sha256.Sum256(document)
	return Verified{Document: parsed, Signed: signed, RootKeyID: signed.RootKeyID, DocumentSHA256: hex.EncodeToString(sum[:])}, nil
}

func canonicalBase64(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, fmt.Errorf("invalid canonical base64")
	}
	return decoded, nil
}
