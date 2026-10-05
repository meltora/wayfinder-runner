package agentpack

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/safeio"
)

const (
	signingKeyMaxBytes = 64 << 10
	signingKeyPEMType  = "PRIVATE KEY"
)

type FileSigner struct {
	key ed25519.PrivateKey
}

func signingKeyError(path, reason string) error {
	return fmt.Errorf("Agent Pack signing key %q: %s", path, reason)
}

func NewFileSigner(path string) (*FileSigner, error) {
	raw, err := safeio.ReadRegularFile(path, signingKeyMaxBytes, 0o077)
	if err != nil {
		return nil, signingKeyError(path, "must be an owner-only regular file of at most 64 KiB")
	}
	key, reason := parseSigningKeyPEM(raw)
	clear(raw)
	if reason != "" {
		return nil, signingKeyError(path, reason)
	}
	return &FileSigner{key: key}, nil
}

func parseSigningKeyPEM(raw []byte) (ed25519.PrivateKey, string) {
	if !bytes.HasPrefix(bytes.TrimLeft(raw, " \t\r\n"), []byte("-----BEGIN ")) {
		return nil, "must start with the PEM block"
	}
	block, rest := pem.Decode(raw)
	if block == nil {
		return nil, "is not PEM encoded"
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, "must contain exactly one PEM block"
	}
	if block.Type == "ENCRYPTED PRIVATE KEY" || len(block.Headers) != 0 {
		return nil, "must not be encrypted"
	}
	if block.Type != signingKeyPEMType {
		return nil, "must be a PKCS#8 PRIVATE KEY block"
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	clear(block.Bytes)
	if err != nil {
		return nil, "is not a valid PKCS#8 private key"
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, "must be an Ed25519 private key"
	}
	return key, ""
}

func (signer *FileSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if signer == nil || len(signer.key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Agent Pack signer is not initialized")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return ed25519.Sign(signer.key, message), nil
}

func (signer *FileSigner) PublicKeySPKI() ([]byte, error) {
	if signer == nil || len(signer.key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Agent Pack signer is not initialized")
	}
	public, ok := signer.key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("Agent Pack signer public key is unavailable")
	}
	return x509.MarshalPKIXPublicKey(public)
}

func GenerateFileSigningKey(path string) (*FileSigner, error) {
	if path == "" {
		return nil, fmt.Errorf("Agent Pack signing key path is required")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, signingKeyError(path, "parent directory cannot be created")
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, signingKeyError(path, "parent directory must be an owner-only directory")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, signingKeyError(path, "already exists and is never overwritten")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, signingKeyError(path, "cannot be inspected")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, signingKeyError(path, "key generation failed")
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, signingKeyError(path, "key encoding failed")
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: signingKeyPEMType, Bytes: der})
	clear(der)
	defer clear(encoded)
	temporary, err := os.CreateTemp(directory, ".agentpack-key-*")
	if err != nil {
		return nil, signingKeyError(path, "temporary file cannot be created")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return nil, signingKeyError(path, "temporary file mode cannot be set")
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return nil, signingKeyError(path, "key cannot be written")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, signingKeyError(path, "key cannot be synced")
	}
	if err := temporary.Close(); err != nil {
		return nil, signingKeyError(path, "key cannot be closed")
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, signingKeyError(path, "already exists and is never overwritten")
		}
		return nil, signingKeyError(path, "key cannot be published")
	}
	if err := safeio.SyncDirectory(directory); err != nil {
		return nil, signingKeyError(path, "parent directory cannot be synced")
	}
	return &FileSigner{key: private}, nil
}
