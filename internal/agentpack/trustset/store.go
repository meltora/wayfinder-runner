package trustset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/strictjson"
)

const (
	StateDirectory   = "agent-pack-trust"
	StateFileName    = "state.json"
	stateFormat      = 1
	maxStateBytes    = 256 << 10
	Applied          = ApplyOutcome("applied")
	IgnoredNotNewer  = ApplyOutcome("ignored_not_newer")
	stateFieldFormat = "format"
	stateFieldFloor  = "highestSequence"
	stateFieldSet    = "set"
)

type ApplyOutcome string

type Snapshot struct {
	HighestSequence          int64
	Current                  *Verified
	PersistedSetUnverifiable bool
}

type Store struct {
	stateDir string
	roots    trustroot.Roots
	mu       sync.Mutex
	state    Snapshot
}

type persistedState struct {
	Format          int                        `json:"format"`
	HighestSequence int64                      `json:"highestSequence"`
	Set             *v1.AgentPackTrustedKeySet `json:"set,omitempty"`
}

func OpenStore(stateDir string, roots trustroot.Roots) (*Store, error) {
	store := &Store{stateDir: stateDir, roots: roots}
	raw, err := safeio.ReadRegularFile(filepath.Join(stateDir, StateDirectory, StateFileName), maxStateBytes, 0o077)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the platform trust state: %w", err)
	}
	persisted, err := parseState(raw)
	if err != nil {
		return nil, fmt.Errorf("the platform trust state is corrupt: %w", err)
	}
	store.state.HighestSequence = persisted.HighestSequence
	if persisted.Set == nil {
		return store, nil
	}
	verified, err := Verify(*persisted.Set, roots)
	if err != nil {
		store.state.PersistedSetUnverifiable = true
		return store, nil
	}
	if verified.Document.Sequence != persisted.HighestSequence {
		return nil, fmt.Errorf("the platform trust state is corrupt: the stored set does not carry the highest sequence")
	}
	store.state.Current = &verified
	return store, nil
}

func parseState(raw []byte) (persistedState, error) {
	var out persistedState
	fields, err := strictjson.Object(raw)
	if err != nil {
		return out, err
	}
	if err := strictjson.ExactFields(fields, []string{stateFieldFormat, stateFieldFloor, stateFieldSet}, []string{stateFieldFormat, stateFieldFloor}); err != nil {
		return out, err
	}
	format, err := integer(fields[stateFieldFormat], CodeUnsupportedFormat)
	if err != nil || format != stateFormat {
		return out, fmt.Errorf("unsupported state format")
	}
	out.Format = stateFormat
	floor, err := integer(fields[stateFieldFloor], CodeInvalidSequence)
	if err != nil || floor < 0 || floor > MaxSequence {
		return out, fmt.Errorf("invalid highest sequence")
	}
	out.HighestSequence = floor
	if rawSet, ok := fields[stateFieldSet]; ok {
		set, err := decodeSignedSet(rawSet)
		if err != nil {
			return out, err
		}
		if floor < 1 {
			return out, fmt.Errorf("a stored set requires a highest sequence")
		}
		out.Set = &set
	}
	return out, nil
}

func decodeSignedSet(raw json.RawMessage) (v1.AgentPackTrustedKeySet, error) {
	var set v1.AgentPackTrustedKeySet
	if _, err := strictjson.Object(raw); err != nil {
		return set, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return set, fmt.Errorf("invalid stored set")
	}
	return set, nil
}

func (store *Store) Snapshot() Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.state
}

func (store *Store) Roots() trustroot.Roots {
	return store.roots
}

func (store *Store) Apply(signed v1.AgentPackTrustedKeySet) (Verified, ApplyOutcome, error) {
	verified, err := Verify(signed, store.roots)
	if err != nil {
		return Verified{}, "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if verified.Document.Sequence <= store.state.HighestSequence {
		return verified, IgnoredNotNewer, nil
	}
	encoded, err := json.Marshal(persistedState{Format: stateFormat, HighestSequence: verified.Document.Sequence, Set: &signed})
	if err != nil {
		return Verified{}, "", err
	}
	if err := safeio.AtomicWriteFile(store.stateDir, filepath.Join(StateDirectory, StateFileName), encoded, 0o600); err != nil {
		return Verified{}, "", fmt.Errorf("persist the platform trust state: %w", err)
	}
	store.state = Snapshot{HighestSequence: verified.Document.Sequence, Current: &verified}
	return verified, Applied, nil
}
