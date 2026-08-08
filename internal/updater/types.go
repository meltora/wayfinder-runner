// Package updater implements the privileged, fail-closed runner update transaction.
package updater

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	DefaultVersionsDir        = "/opt/our-runner/versions"
	DefaultStateDir           = "/var/lib/our-runner-updater"
	DefaultCurrentLink        = "/opt/our-runner/current"
	DefaultPreviousLink       = "/opt/our-runner/previous"
	DefaultUpdaterCurrentLink = "/opt/our-runner/updater-current"
	DefaultSocketPath         = "/run/our-runner-updater/control.sock"
	DefaultRunnerService      = "our-runner.service"
	DefaultTrustFile          = "/etc/our-runner/update-trust.json"
)

var (
	ErrAlreadyInstalled    = errors.New("release is already installed")
	ErrEquivocation        = errors.New("release sequence equivocation")
	ErrNoPreparedUpdate    = errors.New("no prepared update")
	ErrTransactionMismatch = errors.New("update transaction mismatch")
	ErrSuppressed          = errors.New("release is suppressed after rollback")
	ErrRateLimited         = errors.New("update preparation is rate limited")
)

type Phase string

const (
	PhaseIdle              Phase = "idle"
	PhasePreparing         Phase = "preparing"
	PhasePrepared          Phase = "prepared"
	PhaseStopping          Phase = "stopping"
	PhaseSwitching         Phase = "switching"
	PhaseStarting          Phase = "starting"
	PhaseAwaitingHealth    Phase = "awaiting_health"
	PhaseRollingBack       Phase = "rolling_back"
	PhaseCommittingUpdater Phase = "committing_updater"
)

var requiredBinaries = []string{"our-runner", "our-runner-updater"}

const maxPersistedErrorBytes = 4096

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToValidUTF8(err.Error(), "�")
	if len(message) <= maxPersistedErrorBytes {
		return message
	}
	return strings.ToValidUTF8(message[:maxPersistedErrorBytes], "")
}

type Config struct {
	Channel              string
	OS                   string
	Arch                 string
	RunnerVersion        string
	UpdaterVersion       string
	VersionsDir          string
	StateDir             string
	CurrentLink          string
	PreviousLink         string
	UpdaterCurrentLink   string
	RunnerService        string
	PrepareTimeout       time.Duration
	HealthTimeout        time.Duration
	MaxArchiveBytes      int64
	MaxFileBytes         int64
	MaxExtractedBytes    int64
	RetainVersions       int
	RequireRootOwnership bool
	MinPrepareInterval   time.Duration
	MaxPrepareBackoff    time.Duration
}

type CatalogQuery struct {
	Channel, OS, Arch                      string
	RunnerVersion, UpdaterVersion          string
	CurrentSequence                        uint64
	CurrentReleaseID, CurrentPayloadDigest string
}
type Release struct {
	ReleaseID     string
	Version       string
	Sequence      uint64
	Channel       string
	OS            string
	Arch          string
	URL           string
	Size          int64
	SHA256        string
	PayloadDigest string
}

type Catalog interface {
	Latest(context.Context, CatalogQuery) (Release, error)
}
type ArtifactFetcher interface {
	Download(context.Context, string, int64, string, io.Writer) error
}
type ServiceController interface {
	Stop(context.Context, string) error
	Start(context.Context, string) error
	MainPID(context.Context, string) (int, error)
}
type Preflighter interface {
	Check(context.Context, string) error
}
type DiskSpace interface{ FreeBytes(string) (uint64, error) }
type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Dependencies struct {
	Catalog   Catalog
	Artifacts ArtifactFetcher
	Service   ServiceController
	Preflight Preflighter
	Clock     Clock
	Disk      DiskSpace
	Failpoint func(string) error
}

type Status struct {
	Phase           Phase     `json:"phase"`
	UpdateID        string    `json:"update_id,omitempty"`
	Version         string    `json:"version,omitempty"`
	ReleaseID       string    `json:"release_id,omitempty"`
	Sequence        uint64    `json:"sequence,omitempty"`
	HealthDeadline  time.Time `json:"health_deadline,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	FailedReleaseID string    `json:"failed_release_id,omitempty"`
}

type durableState struct {
	Schema              int       `json:"schema"`
	Phase               Phase     `json:"phase"`
	UpdateID            string    `json:"update_id,omitempty"`
	Version             string    `json:"version,omitempty"`
	Sequence            uint64    `json:"sequence,omitempty"`
	ReleaseID           string    `json:"release_id,omitempty"`
	PayloadDigest       string    `json:"payload_digest,omitempty"`
	ObservedSequence    uint64    `json:"observed_sequence,omitempty"`
	ObservedReleaseID   string    `json:"observed_release_id,omitempty"`
	ObservedDigest      string    `json:"observed_digest,omitempty"`
	PendingSequence     uint64    `json:"pending_sequence,omitempty"`
	PendingDigest       string    `json:"pending_digest,omitempty"`
	PendingVersion      string    `json:"pending_version,omitempty"`
	PendingPath         string    `json:"pending_path,omitempty"`
	PreviousTarget      string    `json:"previous_target,omitempty"`
	HealthDeadline      time.Time `json:"health_deadline,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	FailedSequence      uint64    `json:"failed_sequence,omitempty"`
	FailedDigest        string    `json:"failed_digest,omitempty"`
	FailedReason        string    `json:"failed_reason,omitempty"`
	FailedReleaseID     string    `json:"failed_release_id,omitempty"`
	LastPrepareAttempt  time.Time `json:"last_prepare_attempt,omitempty"`
	ConsecutiveFailures uint32    `json:"consecutive_failures,omitempty"`
}
