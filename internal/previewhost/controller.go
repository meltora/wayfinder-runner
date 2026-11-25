package previewhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

type DeployRequest struct {
	Operation  v1.PreviewDeployOperation
	Content    v1.PreviewBlueprintContentV1
	Credential *v1.ProviderCredential
}

type DeployResult struct {
	Outcome v1.PreviewDeployOutcome
}

type LifecycleRequest struct {
	Operation v1.PreviewLifecycleOperation
	Host      journal.PreviewHost
}

type LifecycleResult struct {
	Outcome v1.PreviewLifecycleResultOutcome
}

type Controller interface {
	Capability() v1.PreviewHostingCapability
	Deploy(ctx context.Context, request DeployRequest) DeployResult
	ApplyLifecycle(ctx context.Context, request LifecycleRequest) LifecycleResult
	Recover(ctx context.Context, host journal.PreviewHost) error
}

type IngressLocator interface {
	IngressAddress(ctx context.Context, previewID string) (string, error)
}

const (
	LabelPreviewJobNetwork = "com.ourplatform.preview-job-network"
	LabelAttemptID         = "com.ourplatform.attempt-id"
)

func JobNetworkName(runnerID, attemptID string) string {
	digest := sha256.Sum256([]byte(runnerID + "\x00" + attemptID))
	return "our-preview-job-" + hex.EncodeToString(digest[:])[:12]
}

func JobNetworkLabels(runnerID, attemptID string) map[string]string {
	return map[string]string{LabelPreviewJobNetwork: runnerID, LabelAttemptID: attemptID}
}

type JobNetworkAttacher interface {
	AttachJob(ctx context.Context, previewID, attemptID, jobContainer string) error
	DetachJob(ctx context.Context, attemptID string) error
	SweepJobNetworks(ctx context.Context, keep func(attemptID string) bool) error
}
