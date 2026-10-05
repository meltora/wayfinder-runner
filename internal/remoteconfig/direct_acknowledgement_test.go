package remoteconfig

import (
	"context"
	"errors"
	"testing"
)

func acknowledgedState(t *testing.T, cached []byte) Local {
	t.Helper()
	local := testLocal(t)
	local.AcknowledgeDirect = true
	if err := SaveFiles(local.StateDir, local, cached); err != nil {
		t.Fatal(err)
	}
	return local
}

func TestResolveKeepsTheDirectAcknowledgementWhileTheDashboardStaysDirect(t *testing.T) {
	direct := encode(t, deliveryBody(directConfig()))
	local := acknowledgedState(t, direct)
	var gateways []string
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(direct, nil, &gateways), noSleepPolicy())
	if err != nil || result.Merged.Config.Isolation != "direct" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	loaded, err := LoadLocal(local.StateDir)
	if err != nil || !loaded.AcknowledgeDirect {
		t.Fatalf("local = %+v err = %v", loaded, err)
	}
}

func TestResolveWithdrawsTheDirectAcknowledgementOnceTheDashboardDeliversDocker(t *testing.T) {
	local := acknowledgedState(t, encode(t, deliveryBody(directConfig())))
	var gateways []string
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy())
	if err != nil || result.Merged.Config.Isolation != "docker" || result.Merged.Config.AllowUnsafeDirect {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	loaded, err := LoadLocal(local.StateDir)
	if err != nil || loaded.AcknowledgeDirect {
		t.Fatalf("local = %+v err = %v", loaded, err)
	}
	if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(directConfig())), nil, &gateways), noSleepPolicy()); !errors.Is(err, ErrDirectNotAcknowledged) {
		t.Fatalf("docker to direct switch err = %v", err)
	}
}

func TestResolveLeavesTheAcknowledgementAloneWhenItFallsBackToTheCache(t *testing.T) {
	local := acknowledgedState(t, encode(t, deliveryBody(directConfig())))
	var gateways []string
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(nil, &NetworkError{Err: errors.New("refused")}, &gateways), noSleepPolicy())
	if err != nil || !result.FromCache || result.Merged.Config.Isolation != "direct" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	loaded, err := LoadLocal(local.StateDir)
	if err != nil || !loaded.AcknowledgeDirect {
		t.Fatalf("local = %+v err = %v", loaded, err)
	}
}
