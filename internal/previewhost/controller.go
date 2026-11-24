package previewhost

import (
	"context"

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
