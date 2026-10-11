package trustroot

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/example/app-runner/internal/strictjson"
)

const (
	RootsFileFormat   = 1
	MaxRoots          = 2
	MaxRootsFileBytes = 16 << 10
)

var rootKeyIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Root struct {
	KeyID               string
	PublicKeySPKIBase64 string
	PublicKey           ed25519.PublicKey
}

func ParseRootsFile(raw []byte) ([]Root, error) {
	if len(raw) > MaxRootsFileBytes {
		return nil, fmt.Errorf("trust roots file exceeds %d bytes", MaxRootsFileBytes)
	}
	fields, err := strictjson.Object(raw)
	if err != nil {
		return nil, fmt.Errorf("trust roots file: %w", err)
	}
	if err := strictjson.ExactFields(fields, []string{"format", "roots"}, []string{"format", "roots"}); err != nil {
		return nil, fmt.Errorf("trust roots file: %w", err)
	}
	format, err := strictjson.Number(fields["format"])
	if err != nil || format.String() != "1" {
		return nil, fmt.Errorf("trust roots file: unsupported format")
	}
	items, err := strictjson.Array(fields["roots"])
	if err != nil {
		return nil, fmt.Errorf("trust roots file: roots: %w", err)
	}
	if len(items) > MaxRoots {
		return nil, fmt.Errorf("trust roots file: at most %d roots are allowed", MaxRoots)
	}
	out := make([]Root, 0, len(items))
	seen := map[string]bool{}
	for index, item := range items {
		root, err := parseRoot(item)
		if err != nil {
			return nil, fmt.Errorf("trust roots file: root %d: %w", index, err)
		}
		if seen[root.KeyID] {
			return nil, fmt.Errorf("trust roots file: root %s is listed twice", root.KeyID)
		}
		seen[root.KeyID] = true
		out = append(out, root)
	}
	return out, nil
}

func parseRoot(raw []byte) (Root, error) {
	fields, err := strictjson.Object(raw)
	if err != nil {
		return Root{}, err
	}
	if err := strictjson.ExactFields(fields, []string{"keyId", "publicKeySpkiBase64"}, []string{"keyId", "publicKeySpkiBase64"}); err != nil {
		return Root{}, err
	}
	keyID, err := strictjson.String(fields["keyId"])
	if err != nil || !rootKeyIDPattern.MatchString(keyID) {
		return Root{}, fmt.Errorf("keyId must be the lowercase SHA-256 of the root public key")
	}
	encoded, err := strictjson.String(fields["publicKeySpkiBase64"])
	if err != nil {
		return Root{}, fmt.Errorf("publicKeySpkiBase64 must be a string")
	}
	publicKey, fingerprint, err := Ed25519FromSPKIBase64(encoded)
	if err != nil {
		return Root{}, err
	}
	if fingerprint != keyID {
		return Root{}, fmt.Errorf("keyId does not match the root public key")
	}
	return Root{KeyID: keyID, PublicKeySPKIBase64: encoded, PublicKey: publicKey}, nil
}

func Ed25519FromSPKIBase64(encoded string) (ed25519.PublicKey, string, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(der) != encoded {
		return nil, "", fmt.Errorf("public key must be canonical base64")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", fmt.Errorf("public key must be an SPKI-encoded Ed25519 key")
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, "", fmt.Errorf("public key must be an SPKI-encoded Ed25519 key")
	}
	canonical, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil || string(canonical) != string(der) {
		return nil, "", fmt.Errorf("public key must be a canonical SPKI encoding")
	}
	sum := sha256.Sum256(der)
	return publicKey, hex.EncodeToString(sum[:]), nil
}
