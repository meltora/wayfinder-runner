package lease

import (
	"fmt"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type Lease struct {
	LeaseID          string
	JobID            string
	AttemptID        string
	FencingToken     uint64
	Kind             v1.JobKind
	ManifestID       string
	ManifestDigest   string
	WorkflowRevision int
	AcquiredAt       time.Time
	ExpiresAt        time.Time
}

func (l Lease) HasManifestAuthority() bool {
	if validation.ID("job_id", l.JobID) != nil || validation.ID("attempt_id", l.AttemptID) != nil || l.FencingToken == 0 {
		return false
	}
	return validation.ManifestAuthority(v1.ManifestAuthority{
		Kind: l.Kind, LeaseID: l.LeaseID, ManifestID: l.ManifestID,
		ManifestDigest: l.ManifestDigest, WorkflowRevision: l.WorkflowRevision,
	}) == nil
}

type Manager struct {
	mu      sync.Mutex
	current *Lease
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Acquire(l Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !l.HasManifestAuthority() {
		return fmt.Errorf("lease is missing immutable execution authority")
	}
	if m.current != nil && time.Now().Before(m.current.ExpiresAt) {
		if m.current.FencingToken >= l.FencingToken {
			return fmt.Errorf("runner busy: active lease for job %s (fencing %d)", m.current.JobID, m.current.FencingToken)
		}
	}
	m.current = &l
	return nil
}

func (m *Manager) Current() *Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil
	}
	current := *m.current
	return &current
}

func (m *Manager) Validate(attemptID string, fencing uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return fmt.Errorf("no active lease")
	}
	if m.current.AttemptID != attemptID {
		return fmt.Errorf("attempt %s does not own the lease", attemptID)
	}
	if m.current.FencingToken != fencing {
		return fmt.Errorf("stale fencing token %d (current %d)", fencing, m.current.FencingToken)
	}
	if time.Now().After(m.current.ExpiresAt) {
		return fmt.Errorf("lease expired")
	}
	return nil
}

func (m *Manager) Renew(d time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return false
	}
	now := time.Now()
	m.current.AcquiredAt = now
	m.current.ExpiresAt = now.Add(d)
	return true
}

func (m *Manager) RenewUntil(attemptID string, fencing uint64, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.AttemptID != attemptID {
		return fmt.Errorf("attempt %s does not own the lease", attemptID)
	}
	if m.current.FencingToken != fencing {
		return fmt.Errorf("stale fencing token %d", fencing)
	}
	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("renewed lease must expire in the future")
	}
	if !expiresAt.After(m.current.ExpiresAt) {
		if expiresAt.Equal(m.current.ExpiresAt) {
			return nil
		}
		return fmt.Errorf("renewed lease must extend the current expiration")
	}
	if expiresAt.After(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("renewed lease cannot exceed 24 hours")
	}
	m.current.AcquiredAt = time.Now()
	m.current.ExpiresAt = expiresAt
	return nil
}

func (m *Manager) Release(attemptID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.AttemptID != attemptID {
		return false
	}
	m.current = nil
	return true
}
