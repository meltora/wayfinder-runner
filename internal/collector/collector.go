package collector

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

const maxArtifactSize = 256 << 20
const defaultArtifactSpoolSize = 1 << 30

type Store struct {
	Root          string
	mu            sync.Mutex
	maxSpoolBytes int64
}

func NewStore(root string) *Store {
	return &Store{Root: root, maxSpoolBytes: defaultArtifactSpoolSize}
}

func (s *Store) SetMaxSpoolBytes(limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSpoolBytes = limit
}

func (s *Store) SaveArtifact(jobID, attemptID string, fencing uint64, name, contentType string, r io.Reader) (*v1.ArtifactRef, string, error) {
	if err := validation.ID("job_id", jobID); err != nil {
		return nil, "", err
	}
	if err := validation.ID("attempt_id", attemptID); err != nil {
		return nil, "", err
	}
	if err := validation.DisplayName("artifact name", name); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	used, err := artifactBytes(filepath.Join(s.Root, "artifacts"))
	if err != nil {
		return nil, "", err
	}
	remaining := s.maxSpoolBytes - used
	if remaining <= 0 {
		return nil, "", fmt.Errorf("artifact spool quota of %d bytes is exhausted", s.maxSpoolBytes)
	}
	dir := filepath.Join(s.Root, "artifacts", jobID, attemptID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	storageName, err := randomStorageName()
	if err != nil {
		return nil, "", err
	}
	dest := filepath.Join(dir, storageName)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(storageName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", err
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(dest)
		}
	}()
	h := sha256.New()
	copyLimit := int64(maxArtifactSize + 1)
	if remaining < int64(maxArtifactSize) {
		copyLimit = remaining + 1
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, copyLimit))
	if err != nil {
		return nil, "", err
	}
	if n > maxArtifactSize {
		return nil, "", fmt.Errorf("artifact exceeds %d-byte limit", maxArtifactSize)
	}
	if n > remaining {
		return nil, "", fmt.Errorf("artifact exceeds remaining spool quota of %d bytes", remaining)
	}
	if err := f.Sync(); err != nil {
		return nil, "", err
	}
	ref := &v1.ArtifactRef{
		JobID: jobID, AttemptID: attemptID, FencingToken: fencing,
		Name: name, ContentType: contentType,
		DigestSHA256: hex.EncodeToString(h.Sum(nil)), SizeBytes: n,
	}
	keep = true
	return ref, dest, nil
}

func artifactBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact spool contains symbolic link %q", path)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func randomStorageName() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate artifact storage name: %w", err)
	}
	return "artifact-" + hex.EncodeToString(id[:]), nil
}

func (s *Store) SaveCheckpoint(cp v1.Checkpoint) error {
	if err := validation.ID("job_id", cp.JobID); err != nil {
		return err
	}
	if err := validation.ID("attempt_id", cp.AttemptID); err != nil {
		return err
	}
	if cp.FencingToken == 0 || cp.CreatedAt.IsZero() {
		return fmt.Errorf("checkpoint requires fencing token and creation time")
	}
	if cp.TrustedRecovery != nil {
		if err := validation.TrustedRecoveryCheckpoint(*cp.TrustedRecovery); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"plan_step": cp.PlanStep, "branch_state": cp.BranchState, "next_action": cp.NextAction,
	} {
		if len(value) > 64<<10 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("checkpoint %s is invalid", field)
		}
	}
	if cp.ProviderSessionID != "" {
		if err := validation.ID("provider_session_id", cp.ProviderSessionID); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return safeio.AtomicWriteFile(s.Root, filepath.Join("checkpoints", cp.JobID, cp.AttemptID+".json"), raw, 0o600)
}

func (s *Store) LatestCheckpoint(jobID string) (*v1.Checkpoint, error) {
	if err := validation.ID("job_id", jobID); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.Root, "checkpoints", jobID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var latest *v1.Checkpoint
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		attemptID := strings.TrimSuffix(e.Name(), ".json")
		if validation.ID("attempt_id", attemptID) != nil {
			continue
		}
		f, err := safeio.OpenRegular(s.Root, filepath.Join("checkpoints", jobID, e.Name()))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		_ = f.Close()
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		var cp v1.Checkpoint
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cp); err != nil || cp.JobID != jobID || cp.AttemptID != attemptID {
			continue
		}
		if latest == nil || cp.CreatedAt.After(latest.CreatedAt) {
			c := cp
			latest = &c
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no checkpoints for job %s", jobID)
	}
	return latest, nil
}

// CheckpointForAttempt returns the exact durable checkpoint bound to a fenced
// attempt. It never scans another attempt and therefore can be used as a safe
// opaque recovery reference.
func (s *Store) CheckpointForAttempt(jobID, attemptID string, fencingToken uint64) (*v1.Checkpoint, error) {
	if err := validation.ID("job_id", jobID); err != nil {
		return nil, err
	}
	if err := validation.ID("attempt_id", attemptID); err != nil {
		return nil, err
	}
	if fencingToken == 0 {
		return nil, fmt.Errorf("fencing_token must be non-zero")
	}
	f, err := safeio.OpenRegular(s.Root, filepath.Join("checkpoints", jobID, attemptID+".json"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("checkpoint exceeds size limit")
	}
	var checkpoint v1.Checkpoint
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.JobID != jobID || checkpoint.AttemptID != attemptID || checkpoint.FencingToken != fencingToken {
		return nil, fmt.Errorf("checkpoint identity or fence does not match requested attempt")
	}
	return &checkpoint, nil
}

func NewCheckpoint(job v1.JobSpec, step, branchState, next string) v1.Checkpoint {
	return v1.Checkpoint{
		JobID: job.JobID, AttemptID: job.AttemptID, FencingToken: job.FencingToken,
		PlanStep: step, BranchState: branchState, NextAction: next, CreatedAt: time.Now().UTC(),
	}
}
