package remoteconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func noSleepPolicy() RetryPolicy {
	policy := StartPolicy()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	return policy
}

func configuredState(t *testing.T, cached []byte) Local {
	t.Helper()
	local := testLocal(t)
	if err := SaveFiles(local.StateDir, local, cached); err != nil {
		t.Fatal(err)
	}
	return local
}

func fetchReturning(raw []byte, err error, gateways *[]string) func(string) (FetchFunc, error) {
	return func(gateway string) (FetchFunc, error) {
		*gateways = append(*gateways, gateway)
		return func(context.Context) ([]byte, error) { return raw, err }, nil
	}
}

func TestResolveFetchesMergesAndRefreshesTheCache(t *testing.T) {
	cached := encode(t, deliveryBody(directConfig()))
	local := configuredState(t, cached)
	fresh := encode(t, deliveryBody(dockerConfig()))
	var gateways []string
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(fresh, nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if result.FromCache || result.Merged.Config.Isolation != "docker" || len(gateways) != 1 || gateways[0] != local.ControlPlaneURL {
		t.Fatalf("result = %+v gateways=%v", result, gateways)
	}
	stored, err := LoadCachedDelivery(local.StateDir)
	if err != nil || string(stored) != string(fresh) {
		t.Fatal("cache was not refreshed")
	}
}

func TestResolveFallsBackToTheCacheOnlyAfterNetworkFailures(t *testing.T) {
	cached := encode(t, deliveryBody(dockerConfig()))
	for _, failure := range []error{&NetworkError{Err: errors.New("connection refused")}, &StatusError{Status: 503, Code: "unavailable"}, &StatusError{Status: 502}} {
		local := configuredState(t, cached)
		var gateways []string
		result, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(nil, failure, &gateways), noSleepPolicy())
		if err != nil {
			t.Fatalf("%v: %v", failure, err)
		}
		if !result.FromCache || result.FetchErr == nil || result.Merged.Config.Isolation != "docker" {
			t.Fatalf("result = %+v", result)
		}
	}
	for _, failure := range []error{
		&StatusError{Status: 401, Code: "unauthorized"},
		&StatusError{Status: 404, Code: "configuration_not_ready"},
		&StatusError{Status: 409, Code: "direct_isolation_unavailable"},
		&StatusError{Status: 409, Code: "configuration_changed"},
		&StatusError{Status: 429, Code: "rate_limited"},
		errors.New("remote configuration exceeds 1048576 bytes"),
	} {
		local := configuredState(t, cached)
		var gateways []string
		if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(nil, failure, &gateways), noSleepPolicy()); !errors.Is(err, failure) {
			t.Fatalf("%v: err = %v", failure, err)
		}
	}
	local := configuredState(t, cached)
	var gateways []string
	if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning([]byte(`{"unknown":1}`), nil, &gateways), noSleepPolicy()); err == nil {
		t.Fatal("malformed response accepted")
	}
	stored, _ := LoadCachedDelivery(local.StateDir)
	if string(stored) != string(cached) {
		t.Fatal("malformed response replaced the cache")
	}
}

func TestResolveRequiresLocalConfigurationAndACache(t *testing.T) {
	var gateways []string
	if _, err := Resolve(context.Background(), filepath.Join(t.TempDir(), "state"), testRunnerID, fetchReturning(nil, nil, &gateways), noSleepPolicy()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	local := configuredState(t, []byte("{}"))
	if err := os.Remove(filepath.Join(local.StateDir, RemoteFileName)); err != nil {
		t.Fatal(err)
	}
	failure := &NetworkError{Err: errors.New("refused")}
	if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(nil, failure, &gateways), noSleepPolicy()); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveRefusesACachedDocumentForAnotherRunnerOrWithoutDirectAcknowledgement(t *testing.T) {
	other := deliveryBody(dockerConfig())
	other["runner_id"] = "runner-other"
	local := configuredState(t, encode(t, other))
	var gateways []string
	failure := &NetworkError{Err: errors.New("refused")}
	if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(nil, failure, &gateways), noSleepPolicy()); err == nil {
		t.Fatal("accepted a cache for another runner")
	}
	direct := configuredState(t, encode(t, deliveryBody(directConfig())))
	if _, err := Resolve(context.Background(), direct.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(directConfig())), nil, &gateways), noSleepPolicy()); !errors.Is(err, ErrDirectNotAcknowledged) {
		t.Fatalf("err = %v", err)
	}
}
