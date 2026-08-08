package updatecatalog

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
)

type ecdsaASN1Signature struct {
	R, S *big.Int
}

func Sign(manifest Manifest, signers []Signer) ([]byte, error) {
	if len(signers) == 0 || len(signers) > 32 {
		return nil, invalid("1-32 signers are required")
	}
	digest, err := SigningDigest(manifest)
	if err != nil {
		return nil, err
	}
	detached := make([]DetachedSignature, 0, len(signers))
	seen := make(map[string]struct{}, len(signers))
	seenKeys := make(map[string]struct{}, len(signers))
	for _, signer := range signers {
		if !namePattern.MatchString(signer.KeyID) {
			return nil, invalid("invalid signer key_id %q", signer.KeyID)
		}
		if _, exists := seen[signer.KeyID]; exists {
			return nil, invalid("duplicate signer key_id %q", signer.KeyID)
		}
		seen[signer.KeyID] = struct{}{}
		if !isP256PrivateKey(signer.PrivateKey) {
			return nil, invalid("signer %q must use ECDSA P-256", signer.KeyID)
		}
		fingerprint := publicKeyFingerprint(&signer.PrivateKey.PublicKey)
		if _, exists := seenKeys[fingerprint]; exists {
			return nil, invalid("same signing key appears under multiple key IDs")
		}
		seenKeys[fingerprint] = struct{}{}
		signature, err := ecdsa.SignASN1(rand.Reader, signer.PrivateKey, digest[:])
		if err != nil {
			return nil, invalid("sign with %q: %v", signer.KeyID, err)
		}
		detached = append(detached, DetachedSignature{KeyID: signer.KeyID, Value: signature})
	}
	return Assemble(manifest, detached)
}

// Assemble creates the signed base64-payload envelope from signatures returned
// by external signers. Signatures must cover SigningDigest(manifest).
func Assemble(manifest Manifest, signatures []DetachedSignature) ([]byte, error) {
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		return nil, err
	}
	if len(signatures) == 0 || len(signatures) > 32 {
		return nil, invalid("1-32 detached signatures are required")
	}
	wireSignatures := make([]Signature, 0, len(signatures))
	seen := make(map[string]struct{}, len(signatures))
	for _, signature := range signatures {
		if !namePattern.MatchString(signature.KeyID) {
			return nil, invalid("invalid signature key_id %q", signature.KeyID)
		}
		if _, exists := seen[signature.KeyID]; exists {
			return nil, invalid("duplicate signature key_id %q", signature.KeyID)
		}
		seen[signature.KeyID] = struct{}{}
		canonicalDER, err := canonicalizeECDSASignature(signature.Value)
		if err != nil {
			return nil, invalid("signature for %q: %v", signature.KeyID, err)
		}
		wireSignatures = append(wireSignatures, Signature{
			KeyID: signature.KeyID,
			Value: base64.StdEncoding.EncodeToString(canonicalDER),
		})
	}
	sort.Slice(wireSignatures, func(i, j int) bool { return wireSignatures[i].KeyID < wireSignatures[j].KeyID })
	envelope := signedEnvelope{
		Payload:    base64.StdEncoding.EncodeToString(payload),
		Signatures: wireSignatures,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, invalid("encode signed envelope: %v", err)
	}
	return encoded, nil
}

func signingDigestForPayload(payload []byte) [32]byte {
	message := make([]byte, 0, len(SignatureDomain)+1+len(payload))
	message = append(message, SignatureDomain...)
	message = append(message, 0)
	message = append(message, payload...)
	return sha256.Sum256(message)
}

func parseECDSASignature(value []byte) (*big.Int, *big.Int, error) {
	var signature ecdsaASN1Signature
	rest, err := asn1.Unmarshal(value, &signature)
	if err != nil || len(rest) != 0 || signature.R == nil || signature.S == nil {
		return nil, nil, fmt.Errorf("signature is not strict ASN.1 DER")
	}
	order := elliptic.P256().Params().N
	if signature.R.Sign() <= 0 || signature.S.Sign() <= 0 || signature.R.Cmp(order) >= 0 || signature.S.Cmp(order) >= 0 {
		return nil, nil, fmt.Errorf("signature scalar is out of range")
	}
	return new(big.Int).Set(signature.R), new(big.Int).Set(signature.S), nil
}

func canonicalizeECDSASignature(value []byte) ([]byte, error) {
	r, s, err := parseECDSASignature(value)
	if err != nil {
		return nil, err
	}
	order := elliptic.P256().Params().N
	if !isLowS(s) {
		s.Sub(order, s)
	}
	encoded, err := asn1.Marshal(ecdsaASN1Signature{R: r, S: s})
	if err != nil {
		return nil, fmt.Errorf("encode canonical signature: %w", err)
	}
	return encoded, nil
}

func isLowS(s *big.Int) bool {
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(elliptic.P256().Params().N), 1)
	return s.Cmp(halfOrder) <= 0
}

func isP256PublicKey(key *ecdsa.PublicKey) bool {
	if key == nil || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil {
		return false
	}
	return key.Curve.IsOnCurve(key.X, key.Y)
}

func isP256PrivateKey(key *ecdsa.PrivateKey) bool {
	if key == nil || key.D == nil || !isP256PublicKey(&key.PublicKey) {
		return false
	}
	order := elliptic.P256().Params().N
	if key.D.Sign() <= 0 || key.D.Cmp(order) >= 0 {
		return false
	}
	x, y := elliptic.P256().ScalarBaseMult(key.D.Bytes())
	return x.Cmp(key.X) == 0 && y.Cmp(key.Y) == 0
}

func publicKeyFingerprint(key *ecdsa.PublicKey) string {
	return string(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
}
