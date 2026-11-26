package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/isolation"
)

const (
	claudeLoginRefreshWindow = 15 * time.Minute
	claudeLoginRefreshRetry  = 2 * time.Minute
	claudeLoginMaxLifetime   = 24 * time.Hour
)

var errClaudeLoginRefreshUnavailable = errors.New("the scheduled Claude login refresh needs docker isolation on the restricted job network")

type claudeLoginRefresher func(ctx context.Context, refreshToken string, scopes []string) ([]byte, error)

type claudeLoginRefreshHelper interface {
	RefreshLogin(ctx context.Context, in isolation.LoginRefresh) ([]byte, error)
}

type claudeLoginRefreshState struct {
	refreshed   [32]byte
	failedAt    time.Time
	unavailable bool
}

func (d *Daemon) claudeLoginRefresher() claudeLoginRefresher {
	helper, ok := d.ownership.(claudeLoginRefreshHelper)
	if !ok {
		return nil
	}
	return func(ctx context.Context, refreshToken string, scopes []string) ([]byte, error) {
		if d.cfg.Isolation != "docker" || d.cfg.JobNetwork != "restricted" || d.cfg.JobNetworkName == "" {
			return nil, errClaudeLoginRefreshUnavailable
		}
		return helper.RefreshLogin(ctx, isolation.LoginRefresh{
			RunnerID: d.runnerID, Network: d.cfg.JobNetworkName, RefreshToken: refreshToken, Scopes: scopes,
		})
	}
}

func (s *claudeLoginSync) refreshState(dir string) *claudeLoginRefreshState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.refreshes[dir]
	if state == nil {
		state = &claudeLoginRefreshState{}
		s.refreshes[dir] = state
	}
	return state
}

func (s *claudeLoginSync) refreshDue(state *claudeLoginRefreshState, host claudelogin.Login) bool {
	now := s.now()
	return host.HasRefresh && host.RefreshHash != state.refreshed &&
		time.UnixMilli(host.ExpiresAt).Sub(now) <= claudeLoginRefreshWindow &&
		(state.failedAt.IsZero() || now.Sub(state.failedAt) >= claudeLoginRefreshRetry)
}

func (s *claudeLoginSync) beforeStart(ctx context.Context, environment []string) {
	if s == nil {
		return
	}
	dir, err := isolation.HostClaudeConfigDir(environment)
	if err != nil {
		return
	}
	s.refreshHost(ctx, dir)
}

func (s *claudeLoginSync) withHostLock(ctx context.Context, dir string, fn func() error) error {
	unlock, err := claudelogin.LockConfigDir(ctx, dir, s.lock)
	if err != nil {
		return err
	}
	err = fn()
	if unlockErr := unlock(); unlockErr != nil {
		s.logger.Warn("claude login host lock release failed", "err", unlockErr)
	}
	return err
}

func (s *claudeLoginSync) refreshHost(ctx context.Context, dir string) bool {
	if s.refresh == nil {
		return false
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	state := s.refreshState(dir)
	if _, host, err := claudelogin.ReadLogin(dir); err != nil || !s.refreshDue(state, host) {
		return false
	}
	var baseline claudelogin.Login
	var token string
	var scopes []string
	due := false
	err := s.withHostLock(ctx, dir, func() error {
		_, host, err := claudelogin.ReadLogin(dir)
		if err != nil || !s.refreshDue(state, host) {
			return nil
		}
		baseline = host
		token, scopes, err = claudelogin.RefreshGrant(host.OAuth)
		if err != nil {
			state.refreshed = host.RefreshHash
			s.logger.Warn("claude login refresh skipped", "reason", "no_refresh_grant")
			return nil
		}
		due = true
		return nil
	})
	if err != nil {
		s.logger.Warn("claude login refresh skipped", "reason", "locked")
		return false
	}
	if !due {
		return false
	}
	out, err := s.refresh(ctx, token, scopes)
	if errors.Is(err, errClaudeLoginRefreshUnavailable) {
		state.failedAt = s.now()
		if !state.unavailable {
			state.unavailable = true
			s.logger.Info("claude login refresh unavailable", "err", err)
		}
		return false
	}
	if err != nil {
		state.failedAt = s.now()
		s.logger.Warn("claude login refresh failed", "err", err)
		return false
	}
	fresh, err := claudelogin.ParseCredentials(out)
	reason := ""
	switch {
	case err != nil || !fresh.HasRefresh:
		reason = string(claudelogin.ReasonUnparsable)
	case fresh.ExpiresAt <= baseline.ExpiresAt:
		reason = string(claudelogin.ReasonOlder)
	case fresh.Account.Known() && baseline.Account.Known() && fresh.Account != baseline.Account:
		reason = string(claudelogin.ReasonAccountMismatch)
	}
	if reason != "" {
		s.rejectRefresh(state, baseline, reason, nil)
		return false
	}
	if time.UnixMilli(fresh.ExpiresAt).After(s.now().Add(claudeLoginMaxLifetime)) {
		s.logger.Warn("claude login refresh returned an expiry more than 24 hours ahead; writing it anyway", "reason", "implausible_expiry")
	}
	written := false
	err = s.withHostLock(ctx, dir, func() error {
		data, current, err := claudelogin.ReadLogin(dir)
		if err != nil {
			return err
		}
		if current.RefreshHash != baseline.RefreshHash || current.ExpiresAt != baseline.ExpiresAt {
			return nil
		}
		merged, err := claudelogin.MergeCredentials(data, fresh.OAuth)
		if err != nil {
			return err
		}
		if err := claudelogin.WriteFileAtomic(dir, ".credentials.json", merged, -1); err != nil {
			return err
		}
		written = true
		return nil
	})
	if err != nil {
		s.rejectRefresh(state, baseline, "write_failed", err)
		return false
	}
	state.refreshed = baseline.RefreshHash
	state.failedAt = time.Time{}
	if !written {
		s.logger.Warn("claude login refresh dropped because the host login changed meanwhile", "reason", string(claudelogin.ReasonHostChanged))
		return false
	}
	s.logger.Info("claude login refreshed", "expires_in_minutes", int(time.UnixMilli(fresh.ExpiresAt).Sub(s.now()).Minutes()))
	return true
}

func (s *claudeLoginSync) rejectRefresh(state *claudeLoginRefreshState, baseline claudelogin.Login, reason string, err error) {
	state.refreshed = baseline.RefreshHash
	state.failedAt = time.Time{}
	attrs := []any{"reason", reason, "event", "host_login_needs_sign_in"}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	s.logger.Warn("claude login refresh rejected; host Claude login needs a new sign-in", attrs...)
}
