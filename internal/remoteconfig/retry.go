package remoteconfig

import (
	"context"
	"net/http"
	"time"
)

type FetchFunc func(context.Context) ([]byte, error)

type RetryPolicy struct {
	WaitForProfile   bool
	NotReadyInterval time.Duration
	NotReadyTimeout  time.Duration
	ChangedRetries   int
	ChangedDelay     time.Duration
	Sleep            func(context.Context, time.Duration) error
	OnWaiting        func()
}

func ConfigurePolicy() RetryPolicy {
	return RetryPolicy{
		WaitForProfile:   true,
		NotReadyInterval: 5 * time.Second,
		NotReadyTimeout:  2 * time.Minute,
		ChangedRetries:   3,
		ChangedDelay:     2 * time.Second,
		Sleep:            sleepContext,
	}
}

func StartPolicy() RetryPolicy {
	policy := ConfigurePolicy()
	policy.WaitForProfile = false
	return policy
}

func FetchWithRetry(ctx context.Context, fetch FetchFunc, policy RetryPolicy) ([]byte, error) {
	sleep := policy.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	var waited time.Duration
	changedRetries := 0
	announced := false
	for {
		raw, err := fetch(ctx)
		if err == nil {
			return raw, nil
		}
		var delay time.Duration
		switch {
		case policy.WaitForProfile && IsStatus(err, http.StatusNotFound, "configuration_not_ready") && waited+policy.NotReadyInterval <= policy.NotReadyTimeout:
			if !announced && policy.OnWaiting != nil {
				policy.OnWaiting()
			}
			announced = true
			delay = policy.NotReadyInterval
			waited += delay
		case IsStatus(err, http.StatusConflict, "configuration_changed") && changedRetries < policy.ChangedRetries:
			changedRetries++
			delay = policy.ChangedDelay
		default:
			return nil, err
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
