package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	pendingFileName      = "pending-identity.json"
	previousFileName     = "previous-identity.json"
	faultPreviousWritten = "previous_written"
)

type StoredIdentity struct {
	Key     *ecdsa.PrivateKey
	CertPEM []byte
	CAPEM   []byte
}

func (i *StoredIdentity) HasCertificate() bool { return i != nil && len(i.CertPEM) > 0 }

func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func (s *Store) injectFault(step string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(step)
}

func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (s *Store) writeIdentityFile(name string, keyPEM, certPEM, caPEM []byte) error {
	raw, err := json.Marshal(bundle{PrivateKey: string(keyPEM), Certificate: string(certPEM), CACert: string(caPEM)})
	if err != nil {
		return err
	}
	if err := atomicWritePrivate(s.dir, name, raw); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return syncDirectory(s.dir)
}

func (s *Store) readIdentityFile(name string, requireCertificate bool) (*StoredIdentity, error) {
	raw, err := readPrivateFile(s.dir, name, 4<<20)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%s is unusable: %w", name, err)
	}
	unusable := fmt.Errorf("%s is unusable", name)
	var out bundle
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, unusable
	}
	block, _ := pem.Decode([]byte(out.PrivateKey))
	if block == nil {
		return nil, unusable
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, unusable
	}
	if (out.Certificate == "") != (out.CACert == "") {
		return nil, unusable
	}
	if out.Certificate == "" {
		if requireCertificate {
			return nil, fmt.Errorf("%s has no certificate", name)
		}
		return &StoredIdentity{Key: key}, nil
	}
	if _, err := tls.X509KeyPair([]byte(out.Certificate), []byte(out.PrivateKey)); err != nil {
		return nil, unusable
	}
	return &StoredIdentity{Key: key, CertPEM: []byte(out.Certificate), CAPEM: []byte(out.CACert)}, nil
}

func (s *Store) removeIdentityFile(name string) error {
	if err := ensurePrivateDirectory(s.dir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir)
}

func (s *Store) SavePendingKey(key *ecdsa.PrivateKey) error {
	keyPEM, err := encodeKeyPEM(key)
	if err != nil {
		return err
	}
	return s.writeIdentityFile(pendingFileName, keyPEM, nil, nil)
}

func (s *Store) SavePendingCertificate(certPEM, caPEM []byte) error {
	pending, err := s.readIdentityFile(pendingFileName, false)
	if err != nil {
		return fmt.Errorf("load pending identity: %w", err)
	}
	if len(certPEM) == 0 || len(caPEM) == 0 {
		return fmt.Errorf("pending certificate and CA are required")
	}
	keyPEM, err := encodeKeyPEM(pending.Key)
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("pending certificate does not match the pending key")
	}
	return s.writeIdentityFile(pendingFileName, keyPEM, certPEM, caPEM)
}

func (s *Store) LoadPending() (*StoredIdentity, error) {
	return s.readIdentityFile(pendingFileName, false)
}

func (s *Store) LoadPrevious() (*StoredIdentity, error) {
	return s.readIdentityFile(previousFileName, true)
}

func (s *Store) PromotePending() error {
	if _, err := s.readIdentityFile(pendingFileName, true); err != nil {
		return fmt.Errorf("load pending identity: %w", err)
	}
	currentKey, err := s.LoadKey()
	if err != nil {
		return fmt.Errorf("load current key: %w", err)
	}
	currentCert, currentCA, err := s.LoadCert()
	if err != nil {
		return fmt.Errorf("load current certificate: %w", err)
	}
	currentKeyPEM, err := encodeKeyPEM(currentKey)
	if err != nil {
		return err
	}
	if err := s.writeIdentityFile(previousFileName, currentKeyPEM, currentCert, currentCA); err != nil {
		return err
	}
	if err := s.injectFault(faultPreviousWritten); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(s.dir, pendingFileName), filepath.Join(s.dir, bundleFileName)); err != nil {
		return fmt.Errorf("promote pending identity: %w", err)
	}
	if err := syncDirectory(s.dir); err != nil {
		return err
	}
	return s.removeLegacyFiles()
}

func (s *Store) removeLegacyFiles() error {
	for _, legacy := range []string{keyFileName, certFileName, caFileName} {
		if err := os.Remove(filepath.Join(s.dir, legacy)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy identity file: %w", err)
		}
	}
	return nil
}

func (s *Store) AwaitingConfirmation() (bool, error) {
	previous, err := s.LoadPrevious()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	currentCert, _, err := s.LoadCert()
	if err != nil {
		return false, err
	}
	return !bytes.Equal(previous.CertPEM, currentCert), nil
}

func (s *Store) ConfirmPromoted() error {
	return s.removeIdentityFile(previousFileName)
}

func (s *Store) RestorePrevious() error {
	if _, err := s.LoadPrevious(); err != nil {
		return fmt.Errorf("load previous identity: %w", err)
	}
	if err := os.Rename(filepath.Join(s.dir, previousFileName), filepath.Join(s.dir, bundleFileName)); err != nil {
		return fmt.Errorf("restore previous identity: %w", err)
	}
	if err := syncDirectory(s.dir); err != nil {
		return err
	}
	if err := s.removeLegacyFiles(); err != nil {
		return err
	}
	return s.DiscardPending()
}

func (s *Store) DiscardPending() error {
	return s.removeIdentityFile(pendingFileName)
}
