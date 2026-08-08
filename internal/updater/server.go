package updater

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/example/app-runner/internal/updateipc"
)

type Peer struct {
	UID, GID, PID int
}
type PeerProvider interface{ Peer(net.Conn) (Peer, error) }
type Authorizer interface {
	Authorize(context.Context, Peer, updateipc.Action) error
}

type RunnerAuthorizer struct {
	ExpectedUID int
	MainPID     func(context.Context) (int, error)
}

// Authorize relies on kernel SO_PEERCRED plus systemd's exact MainPID. Reading
// /proc/PID/exe under ProtectProc would require the much broader CAP_SYS_PTRACE.
func (a *RunnerAuthorizer) Authorize(ctx context.Context, p Peer, _ updateipc.Action) error {
	if a == nil || a.ExpectedUID < 0 || a.MainPID == nil {
		return fmt.Errorf("peer authorization is not configured")
	}
	if p.UID != a.ExpectedUID {
		return fmt.Errorf("peer uid is not authorized")
	}
	pid, err := a.MainPID(ctx)
	if err != nil {
		return fmt.Errorf("resolve runner MainPID: %w", err)
	}
	if pid <= 0 || p.PID != pid {
		return fmt.Errorf("peer pid is not the runner MainPID")
	}
	return nil
}

type Server struct {
	Coordinator *Coordinator
	Peers       PeerProvider
	Authorizer  Authorizer
	IOTimeout   time.Duration
	Report      func(string, string)
	terminal    chan error
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if s.Coordinator == nil || s.Peers == nil || s.Authorizer == nil {
		return fmt.Errorf("updater server dependencies are required")
	}
	if listener == nil {
		return fmt.Errorf("listener is required")
	}
	if s.IOTimeout <= 0 || s.IOTimeout > 30*time.Second {
		s.IOTimeout = 5 * time.Second
	}
	s.terminal = make(chan error, 1)
	if err := s.Coordinator.Recover(ctx); err != nil {
		s.report("recovery_failed", err)
		return err
	}
	s.armRecoveryTimer(ctx)
	terminalDone := make(chan struct{})
	var terminalErr error
	go func() {
		select {
		case <-ctx.Done():
		case terminalErr = <-s.terminal:
		}
		close(terminalDone)
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-terminalDone:
				return terminalErr
			default:
				return err
			}
		}
		after := s.handle(ctx, conn)
		_ = conn.Close()
		if after != nil {
			go after()
		}
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) func() {
	_ = conn.SetDeadline(time.Now().Add(s.IOTimeout))
	peer, err := s.Peers.Peer(conn)
	if err != nil {
		s.writeError(conn, "unauthorized")
		return nil
	}
	req, err := updateipc.DecodeRequest(conn)
	if err != nil {
		s.writeError(conn, "invalid_request")
		return nil
	}
	if err := s.Authorizer.Authorize(ctx, peer, req.Action); err != nil {
		s.writeError(conn, "unauthorized")
		return nil
	}
	_ = conn.SetDeadline(time.Time{})
	switch req.Action {
	case updateipc.ActionPrepare:
		st, err := s.Coordinator.Prepare(ctx, req.UpdateID)
		if err != nil {
			s.report("prepare_failed", err)
		}
		s.respond(conn, st, err)
		return nil
	case updateipc.ActionStatus:
		st, err := s.Coordinator.Status(ctx)
		if err == nil && st.Phase == PhaseIdle && st.ReleaseID == req.UpdateID {
			_ = updateipc.EncodeResponse(conn, updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: true, Phase: "healthy", UpdateID: req.UpdateID})
			return nil
		}
		if err == nil && st.Phase == PhaseIdle && st.FailedReleaseID == req.UpdateID {
			_ = updateipc.EncodeResponse(conn, updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: true, Phase: "rolled_back", UpdateID: req.UpdateID})
			return nil
		}
		s.respond(conn, st, err)
		return nil
	case updateipc.ActionHealth:
		err := s.Coordinator.Health(ctx, req.UpdateID, req.RunnerVersion)
		if err == nil {
			_ = updateipc.EncodeResponse(conn, updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: true, Phase: "healthy", UpdateID: req.UpdateID})
		} else {
			s.report("health_failed", err)
			st, _ := s.Coordinator.Status(ctx)
			s.respond(conn, st, err)
		}
		if err == nil {
			return func() { s.finish(nil) }
		}
		return nil
	case updateipc.ActionActivate:
		st, err := s.Coordinator.Status(ctx)
		if err != nil || st.Phase != PhasePrepared || st.UpdateID != req.UpdateID {
			s.respond(conn, st, firstError(err, ErrNoPreparedUpdate))
			return nil
		}
		if err := updateipc.EncodeResponse(conn, updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: true, Phase: "activating", UpdateID: req.UpdateID}); err != nil {
			s.report("activation_ack_failed", err)
			return nil
		}
		return func() {
			activated, err := s.Coordinator.Activate(context.Background(), req.UpdateID)
			if err != nil {
				s.report("activation_failed", err)
				s.finish(err)
				return
			}
			s.armTimer(context.Background(), activated.HealthDeadline)
		}
	default:
		s.writeError(conn, "invalid_request")
		return nil
	}
}
func (s *Server) respond(conn net.Conn, st Status, err error) {
	resp := updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: err == nil, Phase: string(st.Phase), UpdateID: st.UpdateID}
	if err != nil {
		resp.ErrorCode = errorCode(err)
	}
	_ = updateipc.EncodeResponse(conn, resp)
}
func (s *Server) writeError(conn net.Conn, code string) {
	_ = updateipc.EncodeResponse(conn, updateipc.Response{SchemaVersion: updateipc.SchemaVersion, OK: false, ErrorCode: code})
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrAlreadyInstalled):
		return "already_installed"
	case errors.Is(err, ErrEquivocation):
		return "equivocation"
	case errors.Is(err, ErrTransactionMismatch):
		return "transaction_mismatch"
	case errors.Is(err, ErrNoPreparedUpdate):
		return "not_prepared"
	case errors.Is(err, ErrSuppressed):
		return "release_suppressed"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	default:
		return "update_failed"
	}
}
func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
func (s *Server) finish(err error) {
	select {
	case s.terminal <- err:
	default:
	}
}
func (s *Server) armRecoveryTimer(ctx context.Context) {
	st, err := s.Coordinator.Status(ctx)
	if err == nil && st.Phase == PhaseAwaitingHealth {
		s.armTimer(ctx, st.HealthDeadline)
	}
}
func (s *Server) armTimer(ctx context.Context, deadline time.Time) {
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	// #nosec G118 -- once the health deadline fires, rollback must complete even
	// if the request/server context is canceled concurrently; service commands
	// inside Recover retain their own strict timeouts.
	go func() {
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := s.Coordinator.Recover(context.Background())
			if err != nil {
				s.report("recovery_failed", err)
			}
			s.finish(err)
		}
	}()
}

var urlInError = regexp.MustCompile(`https?://[^\s"']+`)

func (s *Server) report(event string, err error) {
	if s.Report == nil || err == nil {
		return
	}
	message := urlInError.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		if u, parseErr := url.Parse(raw); parseErr == nil {
			u.RawQuery = ""
			u.User = nil
			u.Fragment = ""
			return u.String()
		}
		return "[redacted-url]"
	})
	s.Report(event, message)
}

func ListenUnix(path string, mode os.FileMode) (net.Listener, error) {
	if path == "" {
		path = DefaultSocketPath
	}
	if len(path) > 100 {
		return nil, fmt.Errorf("socket path too long")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		return nil, errors.Join(err, ln.Close())
	}
	return ln, nil
}
