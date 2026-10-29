package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

// recoverEnrollmentIdentity completes only the local commit after SaveIdentity.
// It neither consumes another enrollment code nor replaces any identity material.
func recoverEnrollmentIdentity(stateDir string, now time.Time) (bool, error) {
	info, err := os.Lstat(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(stateDir) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("enrollment recovery requires a real absolute state directory")
	}
	present := false
	for _, name := range []string{"identity.json", "runner-cert.pem", "ca-cert.pem", "runner-id"} {
		if _, err := os.Lstat(filepath.Join(stateDir, name)); err == nil {
			present = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if !present {
		return false, nil
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("enrollment recovery requires a private state directory")
	}
	store := identity.NewStore(stateDir)
	certPEM, caPEM, err := store.LoadCert()
	if err != nil {
		return false, fmt.Errorf("load saved enrollment identity: %w", err)
	}
	key, err := store.LoadKey()
	if err != nil {
		return false, fmt.Errorf("load saved enrollment key: %w", err)
	}
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return false, fmt.Errorf("saved enrollment certificate is malformed")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("parse saved enrollment certificate: %w", err)
	}
	runnerID := cert.Subject.CommonName
	if err := validation.ID("runner_id", runnerID); err != nil {
		return false, err
	}
	if err := identity.ValidateIssued(certPEM, caPEM, key, runnerID, now); err != nil {
		return false, fmt.Errorf("validate saved enrollment identity: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "runner-id")); err == nil {
		storedID, err := daemon.ReadRunnerID(stateDir)
		if err != nil {
			return false, fmt.Errorf("read existing runner ID: %w", err)
		}
		if storedID != runnerID {
			return false, fmt.Errorf("existing runner ID does not match saved enrollment identity")
		}
		return false, fmt.Errorf("already enrolled; use a fresh state dir or clear existing identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := writeMissingEnrollmentRunnerID(stateDir, runnerID); err != nil {
		return false, fmt.Errorf("complete saved enrollment: %w", err)
	}
	return true, nil
}

// Link publishes a fully synced private file only if runner-id is still absent.
// Unlike rename, it cannot overwrite an ID created by a concurrent command.
func writeMissingEnrollmentRunnerID(stateDir, runnerID string) error {
	if err := validation.ID("runner_id", runnerID); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".enrollment-id-*")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(runnerID + "\n"); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), filepath.Join(stateDir, "runner-id")); err != nil {
		return err
	}
	return safeio.SyncDirectory(stateDir)
}
