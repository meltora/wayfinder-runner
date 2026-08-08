package collector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

type PlanState struct {
	LatestRevision int    `json:"latest_revision"`
	LatestDigest   string `json:"latest_digest"`
}

type PlanStore struct {
	dir string
	mu  sync.Mutex
}

func NewPlanStore(dir string) *PlanStore { return &PlanStore{dir: dir} }

func (s *PlanStore) Get(jobID string) (PlanState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(jobID)
}

func (s *PlanStore) get(jobID string) (PlanState, error) {
	if err := validation.ID("job_id", jobID); err != nil {
		return PlanState{}, err
	}
	f, err := safeio.OpenRegular(s.dir, filepath.Join("plans", jobID+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return PlanState{}, nil
	}
	if err != nil {
		return PlanState{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return PlanState{}, fmt.Errorf("read plan state: invalid size")
	}
	var st PlanState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return st, decoder.Decode(&st)
}

func (s *PlanStore) NextRevision(jobID string, base int, digest string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.get(jobID)
	if err != nil {
		return 0, err
	}
	if base != st.LatestRevision {
		if st.LatestRevision == base+1 && st.LatestDigest == digest {
			return st.LatestRevision, nil
		}
		return 0, fmt.Errorf("stale base revision %d: latest is %d", base, st.LatestRevision)
	}
	next := st.LatestRevision + 1
	st = PlanState{LatestRevision: next, LatestDigest: digest}
	raw, err := json.Marshal(st)
	if err != nil {
		return 0, err
	}
	if err := safeio.AtomicWriteFile(s.dir, filepath.Join("plans", jobID+".json"), raw, 0o600); err != nil {
		return 0, err
	}
	return next, nil
}
