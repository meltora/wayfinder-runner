package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/supervisor"
)

const unprivilegedBubblewrapContainerPath = "PATH=/usr/local/lib/our-runner/unprivileged-bwrap:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type sandboxSettingsSupervisor struct {
	outputTestSupervisor
	sandbox map[string]any
	readErr error
	args    []string
}

func (value *sandboxSettingsSupervisor) Start(ctx context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	value.sandbox, value.readErr = readClaudeSandboxSettings(spec.Args)
	value.args = append([]string(nil), spec.Args...)
	return value.outputTestSupervisor.Start(ctx, spec)
}

func readClaudeSandboxSettings(args []string) (map[string]any, error) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--settings" {
			continue
		}
		raw, err := os.ReadFile(args[index+1])
		if err != nil {
			return nil, err
		}
		var settings struct {
			Sandbox map[string]any `json:"sandbox"`
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, err
		}
		return settings.Sandbox, nil
	}
	return nil, os.ErrNotExist
}

func containerPathArguments(args []string) []string {
	var paths []string
	for index, argument := range args {
		if argument == "-e" && index+1 < len(args) && strings.HasPrefix(args[index+1], "PATH=") {
			paths = append(paths, args[index+1])
		}
	}
	return paths
}

func TestDockerClaudeJobUsesTheWeakerNestedSandbox(t *testing.T) {
	run := newOwnershipRun(t, testIdentityMapping)
	sup := &sandboxSettingsSupervisor{}
	run.jr.d.supervisor = sup
	run.compositionRun.execute(t)
	if sup.readErr != nil {
		t.Fatal(sup.readErr)
	}
	if sup.sandbox["enableWeakerNestedSandbox"] != true || sup.sandbox["failIfUnavailable"] != true {
		t.Fatalf("docker sandbox settings = %v", sup.sandbox)
	}
	if paths := containerPathArguments(sup.args); len(paths) != 1 || paths[0] != unprivilegedBubblewrapContainerPath {
		t.Fatalf("docker claude PATH arguments = %q", paths)
	}
	for _, argument := range sup.args {
		if strings.HasPrefix(argument, "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB") {
			t.Fatalf("docker claude job sets the subprocess environment scrub: %q", sup.args)
		}
	}
}

func TestDirectClaudeJobKeepsTheStrictSandbox(t *testing.T) {
	run := newCompositionRun(t, false)
	var sandbox map[string]any
	var readErr error
	var paths []string
	run.supervisor.duringRun = func(spec supervisor.Spec) {
		sandbox, readErr = readClaudeSandboxSettings(spec.Args)
		paths = containerPathArguments(spec.Args)
	}
	run.execute(t)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, present := sandbox["enableWeakerNestedSandbox"]; present {
		t.Fatalf("direct sandbox settings = %v", sandbox)
	}
	if len(paths) != 0 {
		t.Fatalf("direct claude PATH arguments = %q", paths)
	}
}
