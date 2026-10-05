package remoteconfig

import (
	"context"
	"fmt"
)

type Resolved struct {
	Merged    Merged
	Delivery  Delivery
	Local     Local
	FromCache bool
	FetchErr  error
}

func Resolve(ctx context.Context, stateDir, runnerID string, newFetch func(gatewayURL string) (FetchFunc, error), policy RetryPolicy) (Resolved, error) {
	var out Resolved
	local, err := LoadLocal(stateDir)
	if err != nil {
		return out, err
	}
	fetch, err := newFetch(local.ControlPlaneURL)
	if err != nil {
		return out, err
	}
	raw, err := FetchWithRetry(ctx, fetch, policy)
	if err != nil {
		if !IsTransient(err) {
			return out, err
		}
		cached, cacheErr := LoadCachedDelivery(stateDir)
		if cacheErr != nil {
			return out, fmt.Errorf("%w; no cached runner configuration is available", err)
		}
		raw = cached
		out.FromCache = true
		out.FetchErr = err
	}
	if out.Delivery, err = ParseDelivery(raw, runnerID); err != nil {
		return out, err
	}
	if out.Merged, err = Merge(local, out.Delivery); err != nil {
		return out, err
	}
	if !out.FromCache {
		if out.Delivery.Remote.Isolation != "direct" && local.AcknowledgeDirect {
			local.AcknowledgeDirect = false
			err = SaveFiles(stateDir, local, raw)
		} else {
			err = SaveCachedDelivery(stateDir, raw)
		}
		if err != nil {
			return out, fmt.Errorf("cache remote configuration: %w", err)
		}
	}
	out.Local = local
	return out, nil
}
