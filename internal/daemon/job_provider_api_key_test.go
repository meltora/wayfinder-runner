package daemon

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/apikey"
	"github.com/example/app-runner/internal/redact"
	"github.com/example/app-runner/internal/supervisor"
)

type namedTestSupervisor string

func (name namedTestSupervisor) Name() string { return string(name) }
func (namedTestSupervisor) Available() bool   { return true }
func (namedTestSupervisor) Start(context.Context, supervisor.Spec) (supervisor.Process, error) {
	return nil, nil
}

const daemonTestAPIKey = "daemon-test-api-key-0123456789abcdef"

func TestProviderAPIKeyResolvesForDirectSupervisorAndIsRedacted(t *testing.T) {
	t.Cleanup(redact.ForgetSecretsForTest)
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey)
	jr, _ := testJobRunner(t)
	jr.d.supervisor = namedTestSupervisor("direct")
	jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}

	key, err := jr.d.providerAPIKey("claude")
	if err != nil || key == nil {
		t.Fatalf("key=%v err=%v", key, err)
	}
	if !reflect.DeepEqual(key.entries(), []string{"ANTHROPIC_API_KEY=" + daemonTestAPIKey}) || !reflect.DeepEqual(key.names(), []string{"ANTHROPIC_API_KEY"}) {
		t.Fatalf("entries=%v names=%v", key.entries(), key.names())
	}
	if strings.Contains(redact.KnownSecrets("provider said "+daemonTestAPIKey), daemonTestAPIKey) {
		t.Fatal("resolved key was not registered for redaction")
	}
	if key, err := jr.d.providerAPIKey("codex"); key != nil || err != nil || key.entries() != nil || key.names() != nil {
		t.Fatalf("unconfigured provider key=%v err=%v", key, err)
	}
}

func TestProviderAPIKeyRefusesSupervisorsThatExposeTheEnvironment(t *testing.T) {
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey)
	for _, name := range []string{"tmux", "herdr"} {
		jr, _ := testJobRunner(t)
		jr.d.supervisor = namedTestSupervisor(name)
		jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}
		if _, err := jr.d.providerAPIKey("claude"); err == nil {
			t.Fatalf("%s supervisor accepted an API key", name)
		}
	}
}

func TestProviderAPIKeyErrorsDoNotContainTheKey(t *testing.T) {
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey+" with spaces")
	jr, _ := testJobRunner(t)
	jr.d.supervisor = namedTestSupervisor("direct")
	jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}
	_, err := jr.d.providerAPIKey("claude")
	if err == nil || strings.Contains(err.Error(), daemonTestAPIKey) {
		t.Fatalf("err=%v", err)
	}
}
