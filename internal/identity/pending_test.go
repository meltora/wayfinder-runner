package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/mockcp"
)

type issuedIdentity struct {
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func issueTestIdentity(t *testing.T, ca *mockcp.TestCA, runnerID string) issuedIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := NewCSR(key, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	return issuedIdentity{key: key, certPEM: cert}
}

func enrolledTestStore(t *testing.T) (*Store, *mockcp.TestCA, issuedIdentity) {
	t.Helper()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	current := issueTestIdentity(t, ca, "runner-1")
	s := NewStore(t.TempDir())
	if err := s.SaveIdentity(current.key, current.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	return s, ca, current
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want private regular 0600", filepath.Base(path), info.Mode())
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist: %v", filepath.Base(path), err)
	}
}

func assertCurrentKey(t *testing.T, s *Store, want *ecdsa.PrivateKey) {
	t.Helper()
	got, err := s.LoadKey()
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if !got.Equal(want) {
		t.Fatal("identity.json does not hold the expected key")
	}
}

func TestPendingKeyRoundTripLeavesCurrentIdentity(t *testing.T) {
	t.Parallel()
	s, _, current := enrolledTestStore(t)
	if _, err := s.LoadPending(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadPending without a pending file = %v, want not exist", err)
	}
	next, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingKey(next); err != nil {
		t.Fatalf("SavePendingKey: %v", err)
	}
	assertPrivateFile(t, filepath.Join(s.dir, pendingFileName))
	pending, err := s.LoadPending()
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if !pending.Key.Equal(next) || pending.HasCertificate() {
		t.Fatalf("pending = %+v, want the saved key without a certificate", pending)
	}
	assertCurrentKey(t, s, current.key)
}

func TestSavePendingCertificateRequiresMatchingPendingKey(t *testing.T) {
	t.Parallel()
	s, ca, _ := enrolledTestStore(t)
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err == nil {
		t.Fatal("certificate saved without a pending key")
	}
	other := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingCertificate(other.certPEM, ca.CertPEM); err == nil {
		t.Fatal("certificate for another key was saved as pending")
	}
	pending, err := s.LoadPending()
	if err != nil || pending.HasCertificate() {
		t.Fatalf("mismatched certificate changed the pending file: %+v, %v", pending, err)
	}
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err != nil {
		t.Fatalf("SavePendingCertificate: %v", err)
	}
	pending, err = s.LoadPending()
	if err != nil {
		t.Fatal(err)
	}
	if !pending.HasCertificate() || string(pending.CertPEM) != string(next.certPEM) || string(pending.CAPEM) != string(ca.CertPEM) {
		t.Fatal("pending certificate did not round-trip")
	}
	assertPrivateFile(t, filepath.Join(s.dir, pendingFileName))
}

func TestPromoteConfirmAndRestore(t *testing.T) {
	t.Parallel()
	s, ca, current := enrolledTestStore(t)
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.PromotePending(); err == nil {
		t.Fatal("a pending key without a certificate was promoted")
	}
	assertCurrentKey(t, s, current.key)
	assertAbsent(t, filepath.Join(s.dir, previousFileName))
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if awaiting, err := s.AwaitingConfirmation(); err != nil || awaiting {
		t.Fatalf("AwaitingConfirmation before promotion = %v, %v", awaiting, err)
	}
	if err := s.PromotePending(); err != nil {
		t.Fatalf("PromotePending: %v", err)
	}
	assertCurrentKey(t, s, next.key)
	assertAbsent(t, filepath.Join(s.dir, pendingFileName))
	assertPrivateFile(t, filepath.Join(s.dir, bundleFileName))
	assertPrivateFile(t, filepath.Join(s.dir, previousFileName))
	if err := s.ValidateIdentity("runner-1", time.Now()); err != nil {
		t.Fatalf("promoted identity is invalid: %v", err)
	}
	previous, err := s.LoadPrevious()
	if err != nil || !previous.Key.Equal(current.key) || string(previous.CertPEM) != string(current.certPEM) {
		t.Fatalf("previous identity = %v, %v", previous, err)
	}
	if awaiting, err := s.AwaitingConfirmation(); err != nil || !awaiting {
		t.Fatalf("AwaitingConfirmation after promotion = %v, %v", awaiting, err)
	}

	restored := NewStore(t.TempDir())
	copyDir(t, s.dir, restored.dir)
	stray := issueTestIdentity(t, ca, "runner-1")
	if err := restored.SavePendingKey(stray.key); err != nil {
		t.Fatal(err)
	}
	if err := restored.RestorePrevious(); err != nil {
		t.Fatalf("RestorePrevious: %v", err)
	}
	assertCurrentKey(t, restored, current.key)
	assertAbsent(t, filepath.Join(restored.dir, previousFileName))
	assertAbsent(t, filepath.Join(restored.dir, pendingFileName))
	if err := restored.RestorePrevious(); err == nil {
		t.Fatal("RestorePrevious succeeded twice")
	}

	if err := s.ConfirmPromoted(); err != nil {
		t.Fatalf("ConfirmPromoted: %v", err)
	}
	assertAbsent(t, filepath.Join(s.dir, previousFileName))
	assertCurrentKey(t, s, next.key)
	if err := s.ConfirmPromoted(); err != nil {
		t.Fatalf("ConfirmPromoted is not idempotent: %v", err)
	}
	if awaiting, err := s.AwaitingConfirmation(); err != nil || awaiting {
		t.Fatalf("AwaitingConfirmation after confirmation = %v, %v", awaiting, err)
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscardPendingRemovesOnlyPending(t *testing.T) {
	t.Parallel()
	s, ca, current := enrolledTestStore(t)
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardPending(); err != nil {
		t.Fatalf("DiscardPending: %v", err)
	}
	assertAbsent(t, filepath.Join(s.dir, pendingFileName))
	assertCurrentKey(t, s, current.key)
	if err := s.DiscardPending(); err != nil {
		t.Fatalf("DiscardPending is not idempotent: %v", err)
	}
}

func TestCorruptPendingIsReportedNotUsed(t *testing.T) {
	t.Parallel()
	const marker = "SECRET-MARKER-VALUE"
	cases := map[string]func(t *testing.T, path string){
		"garbage": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not json "+marker), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unknown field": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"private_key_pem":"`+marker+`","extra":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"bad key": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"private_key_pem":"`+marker+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"certificate without CA": func(t *testing.T, path string) {
			ca, err := mockcp.NewTestCA()
			if err != nil {
				t.Fatal(err)
			}
			issued := issueTestIdentity(t, ca, "runner-1")
			keyPEM, err := encodeKeyPEM(issued.key)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(bundle{PrivateKey: string(keyPEM), Certificate: string(issued.certPEM)})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"certificate for another key": func(t *testing.T, path string) {
			ca, err := mockcp.NewTestCA()
			if err != nil {
				t.Fatal(err)
			}
			issued := issueTestIdentity(t, ca, "runner-1")
			other := issueTestIdentity(t, ca, "runner-1")
			keyPEM, err := encodeKeyPEM(other.key)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(bundle{PrivateKey: string(keyPEM), Certificate: string(issued.certPEM), CACert: string(ca.CertPEM)})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"broad permissions": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, current := enrolledTestStore(t)
			write(t, filepath.Join(s.dir, pendingFileName))
			pending, err := s.LoadPending()
			if err == nil || errors.Is(err, os.ErrNotExist) || pending != nil {
				t.Fatalf("LoadPending = %v, %v; want a reported error", pending, err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error discloses file content: %v", err)
			}
			if err := s.PromotePending(); err == nil {
				t.Fatal("a corrupt pending file was promoted")
			}
			assertCurrentKey(t, s, current.key)
		})
	}
}

func TestPromotePendingFromLegacyLayout(t *testing.T) {
	t.Parallel()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(t.TempDir())
	legacy := issueTestIdentity(t, ca, "runner-1")
	if err := s.SaveKey(legacy.key); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCert(legacy.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateIdentity("runner-1", time.Now()); err != nil {
		t.Fatalf("legacy layout does not load: %v", err)
	}
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.PromotePending(); err != nil {
		t.Fatalf("PromotePending: %v", err)
	}
	assertCurrentKey(t, s, next.key)
	for _, name := range []string{keyFileName, certFileName, caFileName} {
		assertAbsent(t, filepath.Join(s.dir, name))
	}
	if err := s.RestorePrevious(); err != nil {
		t.Fatalf("RestorePrevious: %v", err)
	}
	assertCurrentKey(t, s, legacy.key)
	if err := s.ValidateIdentity("runner-1", time.Now()); err != nil {
		t.Fatalf("restored legacy identity is invalid: %v", err)
	}
}

func TestPromotePendingResumesAfterInterruption(t *testing.T) {
	t.Parallel()
	s, ca, current := enrolledTestStore(t)
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	s.fault = func(step string) error {
		if step == faultPreviousWritten {
			return errors.New("interrupted")
		}
		return nil
	}
	if err := s.PromotePending(); err == nil {
		t.Fatal("fault was not injected")
	}
	assertCurrentKey(t, s, current.key)
	if awaiting, err := s.AwaitingConfirmation(); err != nil || awaiting {
		t.Fatalf("an interrupted promotion reads as promoted: %v, %v", awaiting, err)
	}
	s.fault = nil
	if err := s.PromotePending(); err != nil {
		t.Fatalf("resumed PromotePending: %v", err)
	}
	assertCurrentKey(t, s, next.key)
	previous, err := s.LoadPrevious()
	if err != nil || !previous.Key.Equal(current.key) {
		t.Fatalf("previous identity after resume = %v, %v", previous, err)
	}
}

func TestClearRemovesPendingAndPrevious(t *testing.T) {
	t.Parallel()
	s, ca, _ := enrolledTestStore(t)
	next := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(next.key); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingCertificate(next.certPEM, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.PromotePending(); err != nil {
		t.Fatal(err)
	}
	other := issueTestIdentity(t, ca, "runner-1")
	if err := s.SavePendingKey(other.key); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{bundleFileName, pendingFileName, previousFileName} {
		assertAbsent(t, filepath.Join(s.dir, name))
	}
}
