package remoteconfig

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scriptedFetch struct {
	results []error
	calls   int
}

func (s *scriptedFetch) fetch(context.Context) ([]byte, error) {
	index := s.calls
	s.calls++
	if index >= len(s.results) {
		index = len(s.results) - 1
	}
	if s.results[index] != nil {
		return nil, s.results[index]
	}
	return []byte("ok"), nil
}

func recordingSleep(slept *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
}

var (
	notReady = &StatusError{Status: 404, Code: "configuration_not_ready"}
	changed  = &StatusError{Status: 409, Code: "configuration_changed"}
)

func TestFetchWithRetryWaitsForTheDashboardProfileForTwoMinutes(t *testing.T) {
	var slept []time.Duration
	waiting := 0
	policy := ConfigurePolicy()
	policy.Sleep = recordingSleep(&slept)
	policy.OnWaiting = func() { waiting++ }
	script := &scriptedFetch{results: []error{notReady, notReady, nil}}
	raw, err := FetchWithRetry(context.Background(), script.fetch, policy)
	if err != nil || string(raw) != "ok" {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	if script.calls != 3 || waiting != 1 || len(slept) != 2 || slept[0] != 5*time.Second {
		t.Fatalf("calls=%d waiting=%d slept=%v", script.calls, waiting, slept)
	}
	slept = nil
	script = &scriptedFetch{results: []error{notReady}}
	if _, err := FetchWithRetry(context.Background(), script.fetch, policy); !errors.Is(err, notReady) {
		t.Fatalf("err = %v", err)
	}
	if script.calls != 25 || len(slept) != 24 {
		t.Fatalf("calls=%d slept=%d", script.calls, len(slept))
	}
}

func TestFetchWithRetryRetriesConfigurationChangedThreeTimes(t *testing.T) {
	var slept []time.Duration
	policy := StartPolicy()
	policy.Sleep = recordingSleep(&slept)
	script := &scriptedFetch{results: []error{changed, changed, changed, nil}}
	if _, err := FetchWithRetry(context.Background(), script.fetch, policy); err != nil {
		t.Fatal(err)
	}
	if script.calls != 4 || len(slept) != 3 || slept[0] != 2*time.Second {
		t.Fatalf("calls=%d slept=%v", script.calls, slept)
	}
	slept = nil
	script = &scriptedFetch{results: []error{changed}}
	if _, err := FetchWithRetry(context.Background(), script.fetch, policy); !errors.Is(err, changed) {
		t.Fatalf("err = %v", err)
	}
	if script.calls != 4 {
		t.Fatalf("calls = %d", script.calls)
	}
}

func TestFetchWithRetryDoesNotWaitWhenStartingOrOnOtherErrors(t *testing.T) {
	var slept []time.Duration
	policy := StartPolicy()
	policy.Sleep = recordingSleep(&slept)
	for _, failure := range []error{notReady, &StatusError{Status: 401}, &StatusError{Status: 409, Code: "configuration_conflict"}, &NetworkError{Err: errors.New("refused")}} {
		script := &scriptedFetch{results: []error{failure, nil}}
		if _, err := FetchWithRetry(context.Background(), script.fetch, policy); !errors.Is(err, failure) {
			t.Fatalf("err = %v", err)
		}
		if script.calls != 1 {
			t.Fatalf("%v retried", failure)
		}
	}
	if len(slept) != 0 {
		t.Fatalf("slept %v", slept)
	}
}

func TestFetchWithRetryStopsWhenTheContextEnds(t *testing.T) {
	policy := ConfigurePolicy()
	policy.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	script := &scriptedFetch{results: []error{notReady}}
	if _, err := FetchWithRetry(context.Background(), script.fetch, policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
