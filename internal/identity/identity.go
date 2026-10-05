package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	keyFileName    = "runner-key.pem"
	certFileName   = "runner-cert.pem"
	caFileName     = "ca-cert.pem"
	bundleFileName = "identity.json"
)

type bundle struct {
	PrivateKey  string `json:"private_key_pem"`
	Certificate string `json:"certificate_pem"`
	CACert      string `json:"ca_certificate_pem"`
}

type Store struct {
	dir   string
	fault func(step string) error
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) EnsureKey() (*ecdsa.PrivateKey, error) {
	if key, err := s.LoadKey(); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load existing runner key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := ensurePrivateDirectory(s.dir); err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := atomicWritePrivate(s.dir, keyFileName, pemBytes); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	return key, nil
}

func (s *Store) LoadKey() (*ecdsa.PrivateKey, error) {
	data, err := s.loadKeyPEM()
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid key PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func (s *Store) loadKeyPEM() ([]byte, error) {
	if b, err := s.loadBundle(); err == nil {
		return []byte(b.PrivateKey), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return readPrivateFile(s.dir, keyFileName, 1<<20)
}

func (s *Store) SaveKey(key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(s.dir); err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return atomicWritePrivate(s.dir, keyFileName, pemBytes)
}

func (s *Store) SaveCert(certPEM, caPEM []byte) error {
	if err := ensurePrivateDirectory(s.dir); err != nil {
		return err
	}
	if err := atomicWritePrivate(s.dir, certFileName, certPEM); err != nil {
		return err
	}
	return atomicWritePrivate(s.dir, caFileName, caPEM)
}

// SaveIdentity atomically replaces the complete key/certificate/CA tuple.
// Readers observe either the previous generation or the new generation.
func (s *Store) SaveIdentity(key *ecdsa.PrivateKey, certPEM, caPEM []byte) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("identity keypair mismatch: %w", err)
	}
	if err := ensurePrivateDirectory(s.dir); err != nil {
		return err
	}
	raw, err := json.Marshal(bundle{PrivateKey: string(keyPEM), Certificate: string(certPEM), CACert: string(caPEM)})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".identity-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(s.dir, bundleFileName)); err != nil {
		return err
	}
	committed = true
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	for _, legacy := range []string{keyFileName, certFileName, caFileName} {
		if err := os.Remove(filepath.Join(s.dir, legacy)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy identity file: %w", err)
		}
	}
	return nil
}

type directoryCheck func(dir string) error

func (s *Store) loadBundle() (bundle, error) {
	return s.loadBundleChecked(ensurePrivateDirectory)
}

func (s *Store) loadBundleChecked(check directoryCheck) (bundle, error) {
	var out bundle
	raw, err := readPrivateFileChecked(check, s.dir, bundleFileName, 4<<20)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("parse identity bundle: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return out, fmt.Errorf("parse identity bundle: trailing JSON")
	}
	if out.PrivateKey == "" || out.Certificate == "" || out.CACert == "" {
		return out, fmt.Errorf("identity bundle is incomplete")
	}
	return out, nil
}

func (s *Store) LoadCert() (certPEM, caPEM []byte, err error) {
	return s.loadCertChecked(ensurePrivateDirectory)
}

func (s *Store) loadCertChecked(check directoryCheck) (certPEM, caPEM []byte, err error) {
	if b, bundleErr := s.loadBundleChecked(check); bundleErr == nil {
		return []byte(b.Certificate), []byte(b.CACert), nil
	} else if !errors.Is(bundleErr, os.ErrNotExist) {
		return nil, nil, bundleErr
	}
	certPEM, err = readPrivateFileChecked(check, s.dir, certFileName, 2<<20)
	if err != nil {
		return nil, nil, err
	}
	caPEM, err = readPrivateFileChecked(check, s.dir, caFileName, 2<<20)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, caPEM, nil
}

func checkPrivateDirectoryPath(dir string) error {
	clean := filepath.Clean(dir)
	root := filepath.VolumeName(clean) + string(filepath.Separator)
	home, _ := os.UserHomeDir()
	if !filepath.IsAbs(clean) || clean == root || filepath.Dir(clean) == root || (home != "" && clean == filepath.Clean(home)) {
		return fmt.Errorf("identity directory must be a dedicated absolute state directory")
	}
	return nil
}

func inspectPrivateDirectory(dir string) error {
	if err := checkPrivateDirectoryPath(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("identity directory must be a real directory")
	}
	return nil
}

func ensurePrivateDirectory(dir string) error {
	if err := checkPrivateDirectoryPath(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("identity directory must be a private directory with mode 0700 or stricter")
	}
	if info.Mode().Perm()&0o077 != 0 {
		// #nosec G302 -- identity storage is a private directory; owner traversal requires 0700.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure identity directory: %w", err)
		}
	}
	return nil
}

func readPrivateFile(dir, name string, maxSize int64) ([]byte, error) {
	return readPrivateFileChecked(ensurePrivateDirectory, dir, name, maxSize)
}

func readPrivateFileChecked(check directoryCheck, dir, name string, maxSize int64) ([]byte, error) {
	if err := check(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxSize {
		return nil, fmt.Errorf("identity file %s must be a private regular file within its size limit", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, maxSize+1))
}

func atomicWritePrivate(dir, name string, data []byte) error {
	if err := ensurePrivateDirectory(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".identity-file-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, name))
}

func (s *Store) Enrolled() bool {
	_, _, err := s.LoadCert()
	return err == nil
}

func (s *Store) EnrolledReadOnly() bool {
	_, _, err := s.loadCertChecked(inspectPrivateDirectory)
	return err == nil
}

func (s *Store) CertExpiry() (notBefore, notAfter time.Time, err error) {
	certPEM, _, err := s.LoadCert()
	if err != nil {
		return notBefore, notAfter, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return notBefore, notAfter, fmt.Errorf("invalid cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return notBefore, notAfter, err
	}
	return cert.NotBefore, cert.NotAfter, nil
}

// ValidateIdentity verifies the complete stored identity tuple, including the
// private key match, CA chain, client-auth usage, validity window, and runner
// identity binding.
func (s *Store) ValidateIdentity(expectedRunnerID string, now time.Time) error {
	key, err := s.LoadKey()
	if err != nil {
		return fmt.Errorf("load runner private key: %w", err)
	}
	certPEM, caPEM, err := s.LoadCert()
	if err != nil {
		return fmt.Errorf("load runner certificate: %w", err)
	}
	return ValidateIssued(certPEM, caPEM, key, expectedRunnerID, now)
}

func (s *Store) ShouldRotate(now time.Time) (bool, error) {
	notBefore, notAfter, err := s.CertExpiry()
	if err != nil {
		return false, err
	}
	total := notAfter.Sub(notBefore)
	remaining := notAfter.Sub(now)
	return remaining < total/2, nil
}

func (s *Store) Clear() error {
	for _, f := range []string{keyFileName, certFileName, caFileName, bundleFileName, pendingFileName, previousFileName} {
		if err := os.Remove(filepath.Join(s.dir, f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ValidateIssued verifies that a control-plane response is a currently valid
// client certificate for the expected runner and the newly generated key.
func ValidateIssued(certPEM, caPEM []byte, key *ecdsa.PrivateKey, expectedRunnerID string, now time.Time) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return fmt.Errorf("issued certificate does not match generated key")
	}
	if cert.Subject.CommonName != expectedRunnerID {
		return fmt.Errorf("issued certificate runner ID %q does not match %q", cert.Subject.CommonName, expectedRunnerID)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("invalid CA PEM")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("verify issued certificate: %w", err)
	}
	return nil
}

func NewCSR(key *ecdsa.PrivateKey, runnerHint string) ([]byte, error) {
	tmpl := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   runnerHint,
			Organization: []string{"app-runner"},
		},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}
