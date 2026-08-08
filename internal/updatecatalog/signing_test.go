package updatecatalog

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func TestDetachedExternalSignerAssembly(t *testing.T) {
	keyA, keyB := newKey(t), newKey(t)
	manifest := validManifest()
	digest, err := SigningDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sigA, err := ecdsa.SignASN1(rand.Reader, keyA, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sigB, err := ecdsa.SignASN1(rand.Reader, keyB, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Assemble(manifest, []DetachedSignature{
		{KeyID: "key-b", Value: sigB},
		{KeyID: "key-a", Value: sigA},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	policy := policyFor(keyA, keyB)
	verified, err := ParseAndVerify(envelope, policy)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	payloadDigest, _ := PayloadDigest(manifest)
	if verified.PayloadSHA256 != hex.EncodeToString(payloadDigest[:]) {
		t.Fatalf("payload digest = %s", verified.PayloadSHA256)
	}
}

func TestAssembleRejectsDuplicateOrMalformedDetachedSignatures(t *testing.T) {
	key := newKey(t)
	digest, _ := SigningDigest(validManifest())
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if _, err := Assemble(validManifest(), []DetachedSignature{
		{KeyID: "key-a", Value: sig},
		{KeyID: "key-a", Value: sig},
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate signer got %v", err)
	}
	if _, err := Assemble(validManifest(), []DetachedSignature{{KeyID: "bad key", Value: sig}}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("bad signer id got %v", err)
	}
	if _, err := Assemble(validManifest(), []DetachedSignature{{KeyID: "key-a", Value: []byte("bad")}}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("bad signature got %v", err)
	}
}

func TestSignRejectsDuplicateSignerAndNonP256(t *testing.T) {
	key := newKey(t)
	if _, err := Sign(validManifest(), []Signer{
		{KeyID: "key-a", PrivateKey: key},
		{KeyID: "key-a", PrivateKey: key},
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate signer got %v", err)
	}
	if _, err := Sign(validManifest(), nil); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("empty signer list got %v", err)
	}
	if _, err := Sign(validManifest(), []Signer{
		{KeyID: "key-a", PrivateKey: key},
		{KeyID: "key-b", PrivateKey: key},
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("same key under multiple ids got %v", err)
	}
	if _, err := Sign(validManifest(), []Signer{{KeyID: "key-a"}}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("nil key got %v", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sign(validManifest(), []Signer{{KeyID: "key-a", PrivateKey: p384}}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("P-384 key got %v", err)
	}
}

func TestVerifierRequiresCanonicalSignatureOrderAndLowS(t *testing.T) {
	keyA, keyB := newKey(t), newKey(t)
	encoded := signWith(t, validManifest(), keyA, keyB)
	var envelope signedEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Signatures[0], envelope.Signatures[1] = envelope.Signatures[1], envelope.Signatures[0]
	unsorted, _ := json.Marshal(envelope)
	policy := policyFor(keyA, keyB)
	policy.Trust.Threshold = 1
	if _, err := ParseAndVerify(unsorted, policy); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unsorted signatures got %v", err)
	}

	envelope.Signatures[0], envelope.Signatures[1] = envelope.Signatures[1], envelope.Signatures[0]
	der, err := base64.StdEncoding.DecodeString(envelope.Signatures[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	r, s, err := parseECDSASignature(der)
	if err != nil {
		t.Fatal(err)
	}
	if isLowS(s) {
		s.Sub(elliptic.P256().Params().N, s)
	}
	highDER, err := asn1.Marshal(ecdsaASN1Signature{R: r, S: s})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Signatures[0].Value = base64.StdEncoding.EncodeToString(highDER)
	highS, _ := json.Marshal(envelope)
	if _, err := ParseAndVerify(highS, policy); !errors.Is(err, ErrMalformed) {
		t.Fatalf("high-S signature got %v", err)
	}
}
