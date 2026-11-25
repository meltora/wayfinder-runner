package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/remoteconfig"
)

func statusReport(stateDir string, now time.Time) ([]string, error) {
	enrolled := identity.NewStore(stateDir).EnrolledReadOnly()
	lines := []string{
		fmt.Sprint("state dir: ", stateDir),
		fmt.Sprint("enrolled: ", enrolled),
	}
	if runnerID, err := daemon.ReadRunnerID(stateDir); err == nil {
		lines = append(lines, "runner id: "+runnerID, previewHostingStatus(stateDir))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	summary, err := journal.ReadRejectionSummary(ctx, filepath.Join(stateDir, "journal.db"), now)
	if errors.Is(err, os.ErrNotExist) {
		return append(lines, "journal: not created yet"), nil
	}
	if err != nil {
		return append(lines, "journal: unavailable ("+journalErrorClass(err)+")"), nil
	}
	lines = append(lines,
		fmt.Sprintf("unacknowledged events: %d", summary.UnacknowledgedEvents),
		fmt.Sprintf("rejected events: %d", summary.RejectedEvents),
		fmt.Sprintf("rejected for more than 24 hours (may never be accepted): %d", summary.LongRejectedEvents),
	)
	if summary.RejectedEvents > 0 && summary.LastRejection != nil {
		lines = append(lines, fmt.Sprintf("last rejection: %s at %s",
			summary.LastRejection.Code, summary.LastRejection.At.UTC().Format(time.RFC3339)))
	}
	for _, rejection := range summary.SessionRejections {
		lines = append(lines, fmt.Sprintf("gateway closed the session: %s ×%d (last %s)",
			rejection.Code, rejection.Count, rejection.LastRejectedAt.UTC().Format(time.RFC3339)))
	}
	return lines, nil
}

func previewHostingStatus(stateDir string) string {
	local, err := remoteconfig.LoadLocal(stateDir)
	switch {
	case errors.Is(err, remoteconfig.ErrNotConfigured):
		return "preview hosting: off (no " + remoteconfig.LocalFileName + "; run our-runner configure --preview-hosting on, or set preview_hosting.enabled in the --config file)"
	case err != nil:
		return "preview hosting: unknown (" + remoteconfig.LocalFileName + " could not be read; the runner will not start until it is fixed)"
	case !local.PreviewHosting.Enabled:
		return "preview hosting: off (not enabled in " + remoteconfig.LocalFileName + "; run our-runner configure --preview-hosting on)"
	}
	hosting := config.Normalize(config.Config{PreviewHosting: local.PreviewHosting}).PreviewHosting
	return fmt.Sprintf("preview hosting: on (at most %d previews at once)", hosting.MaxConcurrent)
}

func journalErrorClass(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, journal.ErrWALIndexMissing):
		return "wal index missing"
	case strings.Contains(message, "regular file"):
		return "not a regular file"
	case errors.Is(err, os.ErrPermission) || strings.Contains(message, "permission denied") ||
		strings.Contains(message, "readonly") || strings.Contains(message, "unable to open"):
		return "permission denied"
	case strings.Contains(message, "locked") || strings.Contains(message, "busy"):
		return "busy"
	case strings.Contains(message, "not a database") || strings.Contains(message, "malformed") ||
		strings.Contains(message, "corrupt"):
		return "corrupt"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	default:
		return "read failed"
	}
}
