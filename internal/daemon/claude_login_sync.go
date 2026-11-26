package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/isolation"
)

const (
	claudeLoginExchangeInterval  = 2 * time.Minute
	claudeLoginHostCheckInterval = 10 * time.Second
	claudeLoginFinalSyncTimeout  = 30 * time.Second
	claudeLoginMaxLifetime       = 24 * time.Hour
)

type claudeLoginExchanger func(ctx context.Context, attemptID, home string, request claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error)

type claudeLoginSync struct {
	exchange claudeLoginExchanger
	logger   *slog.Logger
	now      func() time.Time
	lock     claudelogin.LockOptions

	mu       sync.Mutex
	jobs     map[string]*claudeLoginJob
	hostSeen map[string]time.Time
}

type claudeLoginJob struct {
	sync      *claudeLoginSync
	attemptID string
	home      isolation.JobHome

	mu           sync.Mutex
	baseline     [32]byte
	lastExchange time.Time
	stopped      bool
	finished     bool
}

type claudeLoginHelper interface {
	ExchangeLogin(ctx context.Context, runnerID, attemptID, home string, in claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error)
}

func (d *Daemon) claudeLoginExchanger() claudeLoginExchanger {
	helper, ok := d.ownership.(claudeLoginHelper)
	if !ok {
		return nil
	}
	return func(ctx context.Context, attemptID, home string, request claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
		return helper.ExchangeLogin(ctx, d.runnerID, attemptID, home, request)
	}
}

func newClaudeLoginSync(exchange claudeLoginExchanger, logger *slog.Logger) *claudeLoginSync {
	if logger == nil {
		logger = slog.Default()
	}
	return &claudeLoginSync{
		exchange: exchange, logger: logger, now: time.Now,
		jobs: map[string]*claudeLoginJob{}, hostSeen: map[string]time.Time{},
	}
}

func (s *claudeLoginSync) register(attemptID string, home isolation.JobHome) *claudeLoginJob {
	if s == nil || !home.SyncsLogin || home.ClaudeConfigDir == "" {
		return nil
	}
	job := &claudeLoginJob{sync: s, attemptID: attemptID, home: home, baseline: home.Baseline, lastExchange: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[attemptID] = job
	if _, seen := s.hostSeen[home.ClaudeConfigDir]; !seen {
		s.hostSeen[home.ClaudeConfigDir] = hostLoginModTime(home.ClaudeConfigDir)
	}
	return job
}

func (s *claudeLoginSync) loop(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(claudeLoginHostCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *claudeLoginSync) tick(ctx context.Context) {
	s.mu.Lock()
	jobs := make([]*claudeLoginJob, 0, len(s.jobs))
	hostDirs := map[string]bool{}
	for _, job := range s.jobs {
		jobs = append(jobs, job)
		hostDirs[job.home.ClaudeConfigDir] = true
	}
	changed := map[string]bool{}
	for dir := range hostDirs {
		modTime := hostLoginModTime(dir)
		if !modTime.Equal(s.hostSeen[dir]) {
			changed[dir] = true
			s.hostSeen[dir] = modTime
		}
	}
	s.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].attemptID < jobs[j].attemptID })
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		job.exchange(ctx, changed[job.home.ClaudeConfigDir])
	}
}

func hostLoginModTime(configDir string) time.Time {
	info, err := os.Stat(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (j *claudeLoginJob) exchange(ctx context.Context, hostChanged bool) {
	s := j.sync
	j.mu.Lock()
	defer j.mu.Unlock()
	if s.exchange == nil || j.stopped || (!hostChanged && s.now().Sub(j.lastExchange) < claudeLoginExchangeInterval) {
		return
	}
	j.lastExchange = s.now()
	_, host, err := claudelogin.ReadLogin(j.home.ClaudeConfigDir)
	if err != nil {
		s.logger.Warn("claude login exchange skipped", "attempt_id", j.attemptID, "reason", string(claudelogin.ReasonUnparsable))
		return
	}
	hostAccount, err := claudelogin.ReadAccount(j.home.ClaudeStateFile)
	if err != nil {
		s.logger.Warn("claude login exchange skipped", "attempt_id", j.attemptID, "reason", string(claudelogin.ReasonUnparsable))
		return
	}
	result, err := s.exchange(ctx, j.attemptID, j.home.Path, claudelogin.ExchangeRequest{Host: host.OAuth, HostAccount: hostAccount, Baseline: j.baseline})
	if err != nil {
		s.logger.Warn("claude login exchange failed", "attempt_id", j.attemptID, "err", err)
		return
	}
	if result.Replaced {
		j.baseline = host.RefreshHash
		s.logger.Info("claude login handed on", "attempt_id", j.attemptID)
		return
	}
	if result.Job == nil {
		return
	}
	candidate, err := claudelogin.ParseOAuth(result.Job)
	if err != nil {
		s.logger.Warn("claude login write-back skipped", "attempt_id", j.attemptID, "reason", string(claudelogin.ReasonUnparsable))
		return
	}
	j.writeBack(ctx, candidate, result.JobAccount)
}

func (j *claudeLoginJob) writeBack(ctx context.Context, candidate claudelogin.Login, account claudelogin.Account) {
	if !candidate.HasRefresh || candidate.RefreshHash == j.baseline {
		return
	}
	s := j.sync
	if time.UnixMilli(candidate.ExpiresAt).After(s.now().Add(claudeLoginMaxLifetime)) {
		s.logger.Warn("claude login write-back skipped", "attempt_id", j.attemptID, "reason", "implausible_expiry")
		return
	}
	hostDir := j.home.ClaudeConfigDir
	unlock, err := claudelogin.LockConfigDir(ctx, hostDir, s.lock)
	if err != nil {
		s.logger.Warn("claude login write-back skipped", "attempt_id", j.attemptID, "reason", "locked")
		return
	}
	defer func() {
		if err := unlock(); err != nil {
			s.logger.Warn("claude login host lock release failed", "err", err)
		}
	}()
	reason := claudelogin.ReasonUnparsable
	data, host, readErr := claudelogin.ReadLogin(hostDir)
	hostAccount, accountErr := claudelogin.ReadAccount(j.home.ClaudeStateFile)
	if readErr == nil && accountErr == nil {
		reason = claudelogin.Decide(candidate, host, j.baseline, account, hostAccount)
	}
	if reason == claudelogin.ReasonOK {
		merged, err := claudelogin.MergeCredentials(data, candidate.OAuth)
		if err == nil {
			err = claudelogin.WriteFileAtomic(hostDir, ".credentials.json", merged, -1)
		}
		if err != nil {
			s.logger.Warn("claude login write-back failed", "attempt_id", j.attemptID, "err", err)
			return
		}
		j.baseline = candidate.RefreshHash
	}
	s.logger.Info("claude login write-back", "attempt_id", j.attemptID, "reason", string(reason))
}

func (j *claudeLoginJob) stop() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.stopped = true
	j.mu.Unlock()
}

func (j *claudeLoginJob) final() {
	if j == nil {
		return
	}
	s := j.sync
	s.mu.Lock()
	if s.jobs[j.attemptID] == j {
		delete(s.jobs, j.attemptID)
	}
	s.mu.Unlock()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stopped = true
	if j.finished {
		return
	}
	j.finished = true
	candidate, account, err := claudelogin.ReadJobLogin(j.home.Path)
	if err != nil {
		s.logger.Warn("claude login final sync skipped", "attempt_id", j.attemptID, "reason", string(claudelogin.ReasonUnparsable))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeLoginFinalSyncTimeout)
	defer cancel()
	j.writeBack(ctx, candidate, account)
}
