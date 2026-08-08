package daemon

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

func marshalKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func newEventID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("evt-%d", timeNowUnixNano())
	}
	return "evt-" + hex.EncodeToString(random[:])
}

func newSecretToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate job token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}
