package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

func savedRecoveryIdentity(t *testing.T, runnerID string, legacy bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	store := identity.NewStore(dir)
	key, err := store.EnsureKey()
	if err != nil {
		t.Fatal(err)
	}
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.NewCSR(key, "local-host")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		err = store.SaveCert(cert, ca.CertPEM)
	} else {
		err = store.SaveIdentity(key, cert, ca.CertPEM)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func recoveryFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			out[entry.Name()] = info.Mode().String()
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = string(raw)
	}
	return out
}

func mutateRecoveryBundle(t *testing.T, dir, key, value string) {
	t.Helper()
	path := filepath.Join(dir, "identity.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields[key] = value
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentRecoveryMissingIDPreservesCompleteIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "bundle", true: "legacy"}[legacy], func(t *testing.T) {
			dir := savedRecoveryIdentity(t, "runner-recovered-42", legacy)
			before := recoveryFiles(t, dir)
			repaired, err := recoverEnrollmentIdentity(dir, time.Now())
			if err != nil || !repaired {
				t.Fatalf("repaired=%v err=%v", repaired, err)
			}
			id, err := daemon.ReadRunnerID(dir)
			if err != nil || id != "runner-recovered-42" {
				t.Fatalf("id=%q err=%v", id, err)
			}
			info, err := os.Stat(filepath.Join(dir, "runner-id"))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private runner-id: %v %v", info, err)
			}
			after := recoveryFiles(t, dir)
			delete(after, "runner-id")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("repair changed identity or unrelated files")
			}
			if err := identity.NewStore(dir).ValidateIdentity(id, time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnrollmentRecoveryRejectsInvalidOrUnrelatedState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, string)
		now    func() time.Time
	}{
		{"matching existing ID", func(t *testing.T, d string) {
			if err := daemon.WriteRunnerID(d, "runner-recovered-42"); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"mismatched existing ID", func(t *testing.T, d string) {
			if err := daemon.WriteRunnerID(d, "other-runner"); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"invalid existing ID", func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "runner-id"), []byte("../other"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"malformed bundle", func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "identity.json"), []byte("{broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"missing certificate", func(t *testing.T, d string) { mutateRecoveryBundle(t, d, "certificate_pem", "") }, time.Now},
		{"malformed certificate", func(t *testing.T, d string) { mutateRecoveryBundle(t, d, "certificate_pem", "not a certificate") }, time.Now},
		{"malformed key", func(t *testing.T, d string) { mutateRecoveryBundle(t, d, "private_key_pem", "not a private key") }, time.Now},
		{"wrong CA", func(t *testing.T, d string) {
			ca, err := mockcp.NewTestCA()
			if err != nil {
				t.Fatal(err)
			}
			mutateRecoveryBundle(t, d, "ca_certificate_pem", string(ca.CertPEM))
		}, time.Now},
		{"expired certificate", func(*testing.T, string) {}, func() time.Time { return time.Now().Add(48 * time.Hour) }},
		{"not yet valid", func(*testing.T, string) {}, func() time.Time { return time.Now().Add(-48 * time.Hour) }},
		{"missing CA legacy", func(t *testing.T, d string) {
			if err := os.Remove(filepath.Join(d, "identity.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "runner-cert.pem"), []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"ID without identity", func(t *testing.T, d string) {
			if err := os.Remove(filepath.Join(d, "identity.json")); err != nil {
				t.Fatal(err)
			}
			if err := daemon.WriteRunnerID(d, "runner-recovered-42"); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"symlink ID", func(t *testing.T, d string) {
			if err := os.Symlink(filepath.Join(d, "absent"), filepath.Join(d, "runner-id")); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
		{"directory ID", func(t *testing.T, d string) {
			if err := os.Mkdir(filepath.Join(d, "runner-id"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, time.Now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := savedRecoveryIdentity(t, "runner-recovered-42", false)
			tc.mutate(t, dir)
			before := recoveryFiles(t, dir)
			if repaired, err := recoverEnrollmentIdentity(dir, tc.now()); err == nil || repaired {
				t.Fatalf("repaired=%v err=%v", repaired, err)
			}
			if !reflect.DeepEqual(before, recoveryFiles(t, dir)) {
				t.Fatal("rejected repair changed state")
			}
		})
	}
}

func TestEnrollmentRecoveryRejectsInvalidCertificateRunnerID(t *testing.T) {
	for _, id := range []string{"", "..", "runner/other", "runner\nother"} {
		dir := savedRecoveryIdentity(t, id, false)
		if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err == nil || repaired {
			t.Fatalf("id=%q repaired=%v err=%v", id, repaired, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "runner-id")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("ID file was written")
		}
	}
}

func TestEnrollmentRecoveryRejectsMismatchedPrivateKey(t *testing.T) {
	dir := savedRecoveryIdentity(t, "runner-recovered-42", true)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.NewStore(dir).SaveKey(key); err != nil {
		t.Fatal(err)
	}
	before := recoveryFiles(t, dir)
	if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err == nil || repaired {
		t.Fatalf("repaired=%v err=%v", repaired, err)
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, dir)) {
		t.Fatal("mismatched identity changed")
	}
}

func TestEnrollmentRecoveryLeavesFreshAndKeyOnlyStateAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err != nil || repaired {
		t.Fatalf("fresh repaired=%v err=%v", repaired, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh recovery created directory")
	}
	if _, err := identity.NewStore(dir).EnsureKey(); err != nil {
		t.Fatal(err)
	}
	before := recoveryFiles(t, dir)
	if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err != nil || repaired {
		t.Fatalf("key-only repaired=%v err=%v", repaired, err)
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, dir)) {
		t.Fatal("key-only state changed")
	}
}

type rejectRecoveryNetwork struct{ calls int }

func (r *rejectRecoveryNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("unexpected network request")
}

func TestEnrollmentRecoveryCommandDoesNotReadCodeOrContactServer(t *testing.T) {
	dir := savedRecoveryIdentity(t, "runner-recovered-42", false)
	transport := &rejectRecoveryNetwork{}
	withDefaultTransport(t, transport)
	err := cmdEnroll([]string{"--state-dir", dir, "--code-file", filepath.Join(t.TempDir(), "missing-code"), "--url", "https://must-not-contact.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 0 {
		t.Fatal("recovery contacted server")
	}
}

func TestEnrollmentRecoveryAtomicIDNeverOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runner-id"), []byte("other-runner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMissingEnrollmentRunnerID(dir, "runner-recovered-42"); err == nil {
		t.Fatal("existing ID overwritten")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runner-id"))
	if err != nil || string(raw) != "other-runner\n" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v %v", entries, err)
	}
}

func TestEnrollmentRecoveryAtomicIDRejectsInvalidInputAndMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := writeMissingEnrollmentRunnerID(dir, "../unrelated"); err == nil {
		t.Fatal("invalid certificate identity accepted")
	}
	if err := writeMissingEnrollmentRunnerID(filepath.Join(dir, "missing"), "runner-recovered-42"); err == nil {
		t.Fatal("writer created an unrelated directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed write changed state: %v %v", entries, err)
	}
}

func TestEnrollmentRecoveryRejectsUnsafeStateDirectoryWithoutChangingIt(t *testing.T) {
	dir := savedRecoveryIdentity(t, "runner-recovered-42", false)
	before := recoveryFiles(t, dir)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err == nil || repaired {
		t.Fatalf("public directory repaired=%v err=%v", repaired, err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatal("recovery changed directory permissions")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if repaired, err := recoverEnrollmentIdentity(link, time.Now()); err == nil || repaired {
		t.Fatalf("linked directory repaired=%v err=%v", repaired, err)
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, dir)) {
		t.Fatal("unsafe path changed identity")
	}
}

func TestEnrollmentRecoveryAllowsNormalEnrollmentFromUnsecuredEmptyOrKeyOnlyDirectory(t *testing.T) {
	for _, keyOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "key-only"}[keyOnly], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if keyOnly {
				if _, err := identity.NewStore(dir).EnsureKey(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if repaired, err := recoverEnrollmentIdentity(dir, time.Now()); err != nil || repaired {
				t.Fatalf("fresh state recovery=%v err=%v", repaired, err)
			}
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatal("recovery changed fresh state")
			}
			codeFile := filepath.Join(t.TempDir(), "code")
			if err := os.WriteFile(codeFile, []byte("code-once"), 0o600); err != nil {
				t.Fatal(err)
			}
			transport := &rejectRecoveryNetwork{}
			withDefaultTransport(t, transport)
			err = cmdEnroll([]string{"--state-dir", dir, "--code-file", codeFile, "--url", "https://in-memory-only.invalid"})
			if err == nil || transport.calls != 1 {
				t.Fatalf("normal enrollment did not reach fake exchange: calls=%d err=%v", transport.calls, err)
			}
			info, err = os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("normal enrollment did not secure state directory")
			}
		})
	}
}

func TestEnrollmentRecoveryCommandRejectsMalformedPendingKeyAndMissingCodeLocally(t *testing.T) {
	for _, malformedKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing code", true: "malformed key"}[malformedKey], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			codeFile := filepath.Join(t.TempDir(), "code")
			if malformedKey {
				if err := os.WriteFile(filepath.Join(dir, "runner-key.pem"), []byte("not a key"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(codeFile, []byte("code-once"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryFiles(t, dir)
			transport := &rejectRecoveryNetwork{}
			withDefaultTransport(t, transport)
			if err := cmdEnroll([]string{"--state-dir", dir, "--code-file", codeFile, "--url", "https://must-not-contact.invalid"}); err == nil {
				t.Fatal("invalid pending state accepted")
			}
			if transport.calls != 0 {
				t.Fatal("invalid pending state contacted server")
			}
			if !reflect.DeepEqual(before, recoveryFiles(t, dir)) {
				t.Fatal("invalid pending state changed")
			}
		})
	}
}
