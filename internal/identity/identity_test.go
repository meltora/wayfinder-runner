package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/app-runner/internal/mockcp"
)

func TestEnsureKeyGeneratesAndReloads(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	k1, err := s.EnsureKey()
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	k2, err := s.EnsureKey()
	if err != nil {
		t.Fatalf("EnsureKey second call: %v", err)
	}
	if !k1.Equal(k2) {
		t.Fatal("expected same key on reload")
	}

	info, err := os.Stat(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestEnsureKeyDoesNotReplaceCorruptIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, keyFileName)
	if err := os.WriteFile(path, []byte("corrupt key"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if _, err := s.EnsureKey(); err == nil {
		t.Fatal("corrupt existing identity must fail closed")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "corrupt key" {
		t.Fatalf("corrupt key was replaced: %q, %v", got, err)
	}
}

func TestEnsureKeyDoesNotFollowSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-key")
	if err := os.Symlink(outside, filepath.Join(dir, keyFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).EnsureKey(); err == nil {
		t.Fatal("key symlink was accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("identity write escaped state directory: %v", err)
	}
}

func TestValidateAndSaveIdentityBundle(t *testing.T) {
	t.Parallel()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := NewCSR(key, "runner-7")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, "runner-7")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateIssued(cert, ca.CertPEM, key, "runner-7", time.Now()); err != nil {
		t.Fatalf("ValidateIssued: %v", err)
	}

	s := NewStore(t.TempDir())
	if err := s.SaveIdentity(key, cert, ca.CertPEM); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if err := s.ValidateIdentity("runner-7", time.Now()); err != nil {
		t.Fatalf("ValidateIdentity: %v", err)
	}
	if err := s.ValidateIdentity("runner-8", time.Now()); err == nil {
		t.Fatal("stored identity accepted for the wrong runner")
	}
	loaded, err := s.LoadKey()
	if err != nil || !loaded.Equal(key) {
		t.Fatalf("LoadKey = %v, %v", loaded, err)
	}
	loadedCert, loadedCA, err := s.LoadCert()
	if err != nil || string(loadedCert) != string(cert) || string(loadedCA) != string(ca.CertPEM) {
		t.Fatal("identity bundle did not round-trip")
	}
	info, err := os.Stat(filepath.Join(s.dir, bundleFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity bundle permissions: %v, %v", info, err)
	}
}

func TestValidateIssuedRejectsWrongRunnerOrKey(t *testing.T) {
	t.Parallel()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := NewCSR(key, "runner-7")
	cert, _ := ca.SignCSR(csr, "runner-7")
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err := ValidateIssued(cert, ca.CertPEM, other, "runner-7", time.Now()); err == nil {
		t.Fatal("mismatched key accepted")
	}
	if err := ValidateIssued(cert, ca.CertPEM, key, "runner-8", time.Now()); err == nil {
		t.Fatal("mismatched runner accepted")
	}
}

func TestCertRoundTripAndClear(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if s.Enrolled() {
		t.Fatal("should not be enrolled before SaveCert")
	}
	if err := s.SaveCert([]byte("cert"), []byte("ca")); err != nil {
		t.Fatalf("SaveCert: %v", err)
	}
	if !s.Enrolled() {
		t.Fatal("should be enrolled after SaveCert")
	}
	cert, ca, err := s.LoadCert()
	if err != nil {
		t.Fatalf("LoadCert: %v", err)
	}
	if string(cert) != "cert" || string(ca) != "ca" {
		t.Fatal("cert/ca mismatch")
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if s.Enrolled() {
		t.Fatal("should not be enrolled after Clear")
	}
}

func TestNewCSR(t *testing.T) {
	s := NewStore(t.TempDir())
	key, err := s.EnsureKey()
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	csrPEM, err := NewCSR(key, "runner-hint")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		t.Fatal("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if csr.Subject.CommonName != "runner-hint" {
		t.Fatalf("CN = %q", csr.Subject.CommonName)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR signature invalid: %v", err)
	}
}

func TestSaveKeyAndCertificateRotationWindow(t *testing.T) {
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	store := NewStore(t.TempDir())
	if err := store.SaveKey(key); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKey()
	if err != nil || !loaded.Equal(key) {
		t.Fatalf("loaded=%v err=%v", loaded, err)
	}
	csr, _ := NewCSR(key, "runner-9")
	cert, err := ca.SignCSR(csr, "runner-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(key, cert, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	notBefore, notAfter, err := store.CertExpiry()
	if err != nil || !notAfter.After(notBefore) {
		t.Fatalf("expiry=%s..%s err=%v", notBefore, notAfter, err)
	}
	rotate, err := store.ShouldRotate(notAfter.Add(-time.Second))
	if err != nil || !rotate {
		t.Fatalf("ShouldRotate=%v err=%v", rotate, err)
	}
	rotate, err = store.ShouldRotate(notBefore)
	if err != nil || rotate {
		t.Fatalf("early ShouldRotate=%v err=%v", rotate, err)
	}
}

func TestIdentityBundleParsingFailsClosed(t *testing.T) {
	cases := map[string]string{
		"invalid json":  `{`,
		"unknown field": `{"private_key_pem":"k","certificate_pem":"c","ca_certificate_pem":"ca","extra":true}`,
		"trailing json": `{"private_key_pem":"k","certificate_pem":"c","ca_certificate_pem":"ca"} {}`,
		"incomplete":    `{"private_key_pem":"k","certificate_pem":"","ca_certificate_pem":"ca"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, bundleFileName), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewStore(dir)
			if _, err := store.LoadKey(); err == nil {
				t.Fatal("malformed bundle key accepted")
			}
			if _, _, err := store.LoadCert(); err == nil {
				t.Fatal("malformed bundle certificate accepted")
			}
		})
	}
}

func TestIdentityStorageRejectsBroadAndUnsafePaths(t *testing.T) {
	if err := ensurePrivateDirectory("relative"); err == nil {
		t.Fatal("relative identity directory accepted")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("symlink identity directory accepted")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, keyFileName)
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(dir, keyFileName, 1024); err == nil {
		t.Fatal("public identity file accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(dir, keyFileName, 2); err == nil {
		t.Fatal("oversized identity file accepted")
	}
}

func TestSaveIdentityRejectsMismatchedKeypair(t *testing.T) {
	ca, _ := mockcp.NewTestCA()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := NewCSR(key, "runner-1")
	cert, _ := ca.SignCSR(csr, "runner-1")
	if err := NewStore(t.TempDir()).SaveIdentity(other, cert, ca.CertPEM); err == nil {
		t.Fatal("mismatched identity saved")
	}
}
