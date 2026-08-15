package hookspool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/hookevent"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

const (
	maxEventBytes        = 256 << 10
	defaultMaxSpoolSize  = 64 << 20
	defaultMaxSpoolFiles = 10_000
)

type Event struct {
	Type           string     `json:"type"`
	Timestamp      time.Time  `json:"timestamp"`
	ManifestKind   v1.JobKind `json:"manifest_kind"`
	LeaseID        string     `json:"lease_id"`
	JobID          string     `json:"job_id"`
	AttemptID      string     `json:"attempt_id"`
	ManifestID     string     `json:"manifest_id"`
	ManifestDigest string     `json:"manifest_digest"`
	FencingToken   uint64     `json:"fencing_token"`
}

var secretKeyPattern = regexp.MustCompile(`(?i)(^|[_-])(api[_-]?key|token|secret|password|cookie|authorization)($|[_-])`)
var secretValuePattern = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/-]{12,}|sk-[a-z0-9_-]{12,}|ghp_[a-z0-9]{20,}|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----)`)

func Redact(data map[string]any) map[string]any {
	return redactMap(data, 0)
}

func redactMap(data map[string]any, depth int) map[string]any {
	out := map[string]any{}
	for k, v := range data {
		if secretKeyPattern.MatchString(k) {
			out[k] = "[redacted]"
			continue
		}
		out[k] = redactValue(v, depth+1)
	}
	return out
}

func redactValue(value any, depth int) any {
	if depth > 6 {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case string:
		if secretValuePattern.MatchString(typed) {
			return "[redacted]"
		}
		if len(typed) > 4096 {
			return typed[:4096] + "…[truncated]"
		}
		return typed
	case map[string]any:
		return redactMap(typed, depth+1)
	case []any:
		limit := len(typed)
		if limit > 256 {
			limit = 256
		}
		out := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			out = append(out, redactValue(item, depth+1))
		}
		return out
	default:
		return value
	}
}

type Spool struct {
	Dir      string
	maxBytes int64
	maxFiles int
}

// appendMu covers the quota check and atomic publication across every Spool
// handle in this runner process. Hook calls construct short-lived handles, so a
// per-handle lock would allow concurrent providers to race past quotas.
var appendMu sync.Mutex

func NewSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Spool{Dir: dir, maxBytes: defaultMaxSpoolSize, maxFiles: defaultMaxSpoolFiles}, nil
}

func (s *Spool) Append(ev Event) error {
	appendMu.Lock()
	defer appendMu.Unlock()
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if err := validateEvent(ev); err != nil {
		return err
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if len(raw) > maxEventBytes {
		return fmt.Errorf("hook event exceeds %d-byte limit", maxEventBytes)
	}
	used, files, err := spoolUsage(s.Dir)
	if err != nil {
		return err
	}
	if used+int64(len(raw)) > s.maxBytes || files >= s.maxFiles {
		return fmt.Errorf("hook event spool quota is exhausted")
	}
	prefix := fmt.Sprintf(".%d-%s-", time.Now().UnixNano(), sanitize(ev.Type))
	tmp, err := os.CreateTemp(s.Dir, prefix)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
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
	name := strings.TrimPrefix(filepath.Base(tmpPath), ".") + ".json"
	if err := os.Rename(tmpPath, filepath.Join(s.Dir, name)); err != nil {
		return err
	}
	return safeio.SyncDirectory(s.Dir)
}

func (s *Spool) Drain(max int) ([]Event, error) {
	var out []Event
	err := s.Process(max, func(ev Event) error {
		out = append(out, ev)
		return nil
	})
	return out, err
}

// Process delivers events in filename order and removes each file only after
// the callback succeeds. A failed delivery remains durable for the next pass.
func (s *Spool) Process(max int, deliver func(Event) error) error {
	if max <= 0 || max > 1000 {
		return fmt.Errorf("drain limit must be between 1 and 1000")
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	processed := 0
	for _, e := range entries {
		if processed >= max {
			break
		}
		isEventFile := strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".jsonl")
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") || !isEventFile {
			continue
		}
		path := filepath.Join(s.Dir, e.Name())
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() <= 0 || info.Size() > maxEventBytes {
			if err := s.quarantine(path, e.Name()); err != nil {
				return err
			}
			continue
		}
		f, err := safeio.OpenRegular(s.Dir, e.Name())
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(f, maxEventBytes))
		sc.Buffer(make([]byte, 0, 16<<10), maxEventBytes)
		var ev Event
		valid := sc.Scan()
		if valid {
			decoder := json.NewDecoder(bytes.NewReader(sc.Bytes()))
			decoder.DisallowUnknownFields()
			valid = decoder.Decode(&ev) == nil && validateEvent(ev) == nil
		}
		if !valid || sc.Scan() || sc.Err() != nil {
			_ = f.Close()
			if err := s.quarantine(path, e.Name()); err != nil {
				return err
			}
			continue
		}
		_ = f.Close()
		if err := deliver(ev); err != nil {
			return err
		}
		if err := safeio.RemoveRegular(s.Dir, e.Name()); err != nil {
			return fmt.Errorf("remove spool file: %w", err)
		}
		processed++
	}
	return nil
}

func validateEvent(ev Event) error {
	if err := hookevent.ValidateType(ev.Type); err != nil {
		return err
	}
	if ev.Timestamp.IsZero() {
		return fmt.Errorf("hook event timestamp is required")
	}
	if err := validation.ManifestAuthority(v1.ManifestAuthority{
		Kind: ev.ManifestKind, LeaseID: ev.LeaseID,
		ManifestID: ev.ManifestID, ManifestDigest: ev.ManifestDigest,
	}); err != nil {
		return err
	}
	for field, value := range map[string]string{"job_id": ev.JobID, "attempt_id": ev.AttemptID} {
		if err := validation.ID(field, value); err != nil {
			return err
		}
	}
	if ev.FencingToken == 0 || ev.FencingToken > 9_007_199_254_740_991 {
		return fmt.Errorf("hook event fencing token is invalid")
	}
	return nil
}

func spoolUsage(dir string) (int64, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var bytes int64
	var files int
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		bytes += info.Size()
		files++
	}
	return bytes, files, nil
}

func (s *Spool) quarantine(path, name string) error {
	dir := filepath.Join(s.Dir, "quarantine")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, fmt.Sprintf("%d-%s.invalid", time.Now().UnixNano(), name))
	if err := os.Rename(path, target); err != nil {
		return fmt.Errorf("quarantine invalid hook event: %w", err)
	}
	return safeio.SyncDirectory(s.Dir)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "event"
	}
	return b.String()
}
