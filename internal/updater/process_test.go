package updater

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCommandPreflightUsesFixedCommandsAndMinimalEnvironment(t *testing.T) {
	t.Parallel()
	var calls []ProcessCall
	p := &CommandPreflight{Timeout: time.Second, RunnerUID: 123, RunnerGID: 456, Run: func(_ context.Context, call ProcessCall) error {
		calls = append(calls, call)
		return nil
	}}
	stage := "/opt/our-runner/versions/v1"
	if err := p.Check(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	for i, name := range requiredBinaries {
		if calls[i].Path != filepath.Join(stage, name) || !reflect.DeepEqual(calls[i].Args, []string{"version"}) {
			t.Fatalf("call=%+v", calls[i])
		}
		if !reflect.DeepEqual(calls[i].Env, minimalEnvironment) {
			t.Fatalf("env=%v", calls[i].Env)
		}
		if !calls[i].DropPrivileges || calls[i].UID != 123 || calls[i].GID != 456 {
			t.Fatalf("credentials=%+v", calls[i])
		}
		if calls[i].Dir != stage {
			t.Fatalf("dir=%q", calls[i].Dir)
		}
	}
}
func TestCommandPreflightFailsClosed(t *testing.T) {
	t.Parallel()
	p := &CommandPreflight{Timeout: time.Millisecond, RunnerUID: 123, RunnerGID: 456, Run: func(context.Context, ProcessCall) error { return errors.New("bad binary") }}
	if err := p.Check(context.Background(), "/slot"); err == nil {
		t.Fatal("preflight failure ignored")
	}
}
func TestCommandPreflightRejectsRootOrMissingCredentials(t *testing.T) {
	t.Parallel()
	p := &CommandPreflight{RunnerUID: 0, RunnerGID: 42, Run: func(context.Context, ProcessCall) error { t.Fatal("candidate executed"); return nil }}
	if err := p.Check(context.Background(), "/slot"); err == nil {
		t.Fatal("root preflight accepted")
	}
}
