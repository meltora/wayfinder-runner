package isolation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func refreshTestOwnership(t *testing.T, state string, run func(args []string) ([]byte, error)) (DockerOwnership, string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "our-runner")
	if err := os.WriteFile(exe, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	return DockerOwnership{
		Mapping: testRootlessMapping, Image: testOwnershipImage, StateDir: state,
		EffectiveUID: func() int { return 1000 },
		Executable:   func() (string, error) { return exe, nil },
		RunIO: func(_ context.Context, _ string, args []string, stdin []byte) ([]byte, error) {
			if len(stdin) != 0 {
				t.Errorf("the refresh helper received stdin")
			}
			return run(args)
		},
	}, exe
}

var refreshTestGrant = LoginRefresh{
	RunnerID: "runner-1", Network: "our-runner-jobs", RefreshToken: "fake-host-refresh",
	Scopes: []string{"user:profile", "user:inference"},
}

func TestRefreshLoginPassesTheGrantOnlyThroughAPrivateEnvFile(t *testing.T) {
	state := t.TempDir()
	var envPath string
	ownership, exe := refreshTestOwnership(t, state, func(args []string) ([]byte, error) {
		for _, arg := range args {
			if strings.Contains(arg, "fake-host-refresh") {
				t.Errorf("argv carries the refresh token: %q", arg)
			}
		}
		envPath = argumentsAfterFlag(args, "--env-file")
		info, err := os.Stat(envPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("env file = %v %v", info, err)
		}
		if dir, err := os.Stat(filepath.Dir(envPath)); err != nil || dir.Mode().Perm() != 0o700 {
			t.Errorf("env dir = %v %v", dir, err)
		}
		data, err := os.ReadFile(envPath)
		if err != nil || string(data) != "CLAUDE_CODE_OAUTH_REFRESH_TOKEN=fake-host-refresh\nCLAUDE_CODE_OAUTH_SCOPES=user:profile user:inference\n" {
			t.Errorf("env file content is not the grant")
		}
		return []byte("{\"claudeAiOauth\":{}}\n"), nil
	})
	out, err := ownership.RefreshLogin(context.Background(), refreshTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"claudeAiOauth":{}}` {
		t.Fatalf("out = %q", out)
	}
	if !strings.HasPrefix(envPath, filepath.Join(state, "login-refresh-")) {
		t.Fatalf("env file %s is outside the state dir", envPath)
	}
	if _, err := os.Stat(filepath.Dir(envPath)); !os.IsNotExist(err) {
		t.Fatalf("the grant directory survived: %v", err)
	}
	_ = exe
}

func TestRefreshLoginArgvIsTheContract(t *testing.T) {
	var got []string
	ownership, exe := refreshTestOwnership(t, t.TempDir(), func(args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return nil, nil
	})
	if _, err := ownership.RefreshLogin(context.Background(), refreshTestGrant); err != nil {
		t.Fatal(err)
	}
	attempt := strings.TrimPrefix(argumentsAfterFlag(got, "--label", "com.ourplatform.attempt-id="), "com.ourplatform.attempt-id=")
	if !strings.HasPrefix(attempt, "login-refresh-") {
		t.Fatalf("attempt label = %q", attempt)
	}
	want := []string{
		"run", "--rm", "--user", "10001:10001", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--network", "our-runner-jobs", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--log-driver", "none",
		"--pids-limit", "256", "--memory", "512m", "--name", ContainerName("runner-1", attempt),
		"--label", "com.ourplatform.runner-id=runner-1",
		"--label", "com.ourplatform.attempt-id=" + attempt,
		"--env-file", argumentsAfterFlag(got, "--env-file"),
		"-v", exe + ":/opt/our-runner-helper/our-runner:ro",
		"--entrypoint", "/opt/our-runner-helper/our-runner",
		testOwnershipImage, "claude-login-refresh",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q", got)
	}
}

func TestRefreshLoginRefusesBadInput(t *testing.T) {
	for name, mutate := range map[string]func(*LoginRefresh, *DockerOwnership){
		"no network":           func(in *LoginRefresh, _ *DockerOwnership) { in.Network = "none" },
		"host network":         func(in *LoginRefresh, _ *DockerOwnership) { in.Network = "host" },
		"no runner":            func(in *LoginRefresh, _ *DockerOwnership) { in.RunnerID = "" },
		"newline in the token": func(in *LoginRefresh, _ *DockerOwnership) { in.RefreshToken = "fake\nHOME=/" },
		"no scopes":            func(in *LoginRefresh, _ *DockerOwnership) { in.Scopes = nil },
		"space in a scope":     func(in *LoginRefresh, _ *DockerOwnership) { in.Scopes = []string{"a b"} },
		"host root":            func(_ *LoginRefresh, o *DockerOwnership) { o.EffectiveUID = func() int { return 0 } },
		"relative state dir":   func(_ *LoginRefresh, o *DockerOwnership) { o.StateDir = "state" },
		"no image":             func(_ *LoginRefresh, o *DockerOwnership) { o.Image = "" },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			ownership, _ := refreshTestOwnership(t, t.TempDir(), func([]string) ([]byte, error) {
				calls++
				return nil, nil
			})
			in := refreshTestGrant
			in.Scopes = slices.Clone(in.Scopes)
			mutate(&in, &ownership)
			_, err := ownership.RefreshLogin(context.Background(), in)
			if err == nil || calls != 0 {
				t.Fatalf("err = %v calls = %d", err, calls)
			}
			if strings.Contains(err.Error(), "fake") {
				t.Fatalf("the error carries the token: %v", err)
			}
		})
	}
}

func TestRefreshLoginRemovesTheGrantWhenTheHelperFails(t *testing.T) {
	state := t.TempDir()
	ownership, _ := refreshTestOwnership(t, state, func([]string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if _, err := ownership.RefreshLogin(context.Background(), refreshTestGrant); err == nil {
		t.Fatal("a failed helper was reported as a refresh")
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("state dir after a failure = %v %v", entries, err)
	}
}

func argumentsAfterFlag(args []string, flag string, prefix ...string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag && (len(prefix) == 0 || strings.HasPrefix(args[index+1], prefix[0])) {
			return args[index+1]
		}
	}
	return ""
}
