package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

var inspectCheckImage = isolation.InspectImage

func checksWithImages(checks map[string]config.CheckConfig) []string {
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(checks)) {
		if checks[id].Image != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func verifyDockerCheckImages(ctx context.Context, checks map[string]config.CheckConfig) error {
	inspected := map[string]bool{}
	for _, id := range checksWithImages(checks) {
		ref := checks[id].Image
		if inspected[ref] {
			continue
		}
		inspected[ref] = true
		inspectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err := inspectCheckImage(inspectCtx, ref)
		cancel()
		if errors.Is(err, isolation.ErrImageNotPresent) {
			return fmt.Errorf("check %s: image %s is not present on the runner's Docker host; pull or build it before starting", id, ref)
		}
		if err != nil {
			return fmt.Errorf("check %s: verify image %s: %w", id, ref, err)
		}
	}
	return nil
}

func warnUnusedCheckImages(checks map[string]config.CheckConfig, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, id := range checksWithImages(checks) {
		logger.Warn("check image is unused under direct isolation; the check runs on this host", "check", id)
	}
}
