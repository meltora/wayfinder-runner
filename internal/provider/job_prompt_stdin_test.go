package provider

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const jobPromptSentinel = "FR08-PROMPT-SENTINEL-9b1c"

func jobPromptStdinParams(t *testing.T, readOnly, resume, hardened, docker bool) StartParams {
	t.Helper()
	p := goldenParams(t, readOnly, hardened)
	if readOnly {
		p.Job.Kind = v1.JobKindPlan
	}
	p.Prompt = jobPromptSentinel + "\n" + strings.Repeat("ticket text ", 64) + jobPromptSentinel
	if resume {
		p.ResumeSessionID = "session-1"
	}
	p.DockerIsolation = docker
	return p
}

func TestJobSpecsDeliverThePromptOnStdinOnly(t *testing.T) {
	for _, adapter := range []Adapter{NewClaude(), NewCodex(), NewOpenCode()} {
		for _, readOnly := range []bool{false, true} {
			for _, resume := range []bool{false, true} {
				for _, hardened := range []bool{false, true} {
					for _, docker := range []bool{false, true} {
						if docker && !hardened {
							continue
						}
						name := fmt.Sprintf("%s/readOnly=%v/resume=%v/hardened=%v/docker=%v", adapter.Name(), readOnly, resume, hardened, docker)
						t.Run(name, func(t *testing.T) {
							p := jobPromptStdinParams(t, readOnly, resume, hardened, docker)
							spec, err := adapter.BuildSpec(p)
							if err != nil {
								t.Fatalf("BuildSpec: %v", err)
							}
							for _, arg := range spec.Args {
								if strings.Contains(arg, jobPromptSentinel) {
									t.Fatalf("argument %q carries the prompt", arg)
								}
							}
							if spec.StdinFile != "" || spec.Stdin == nil {
								t.Fatalf("stdin source = %v file %q", spec.Stdin, spec.StdinFile)
							}
							raw, err := io.ReadAll(spec.Stdin)
							if err != nil || string(raw) != p.Prompt {
								t.Fatalf("stdin = %d bytes err=%v, want the %d-byte prompt", len(raw), err, len(p.Prompt))
							}
							assertStdinPromptArgs(t, adapter.Name(), spec.Args, p)
						})
					}
				}
			}
		}
	}
}

func assertStdinPromptArgs(t *testing.T, adapter string, args []string, p StartParams) {
	t.Helper()
	switch adapter {
	case "claude":
		index := slices.Index(args, "-p")
		if index < 0 || index+2 >= len(args) || args[index+1] != "--input-format" || args[index+2] != "text" {
			t.Fatalf("claude args lack -p --input-format text: %v", args)
		}
		if p.ResumeSessionID != "" && !slices.Equal(args[len(args)-2:], []string{"--resume", p.ResumeSessionID}) {
			t.Fatalf("claude args end %v", args[len(args)-2:])
		}
	case "codex":
		if args[len(args)-1] != "-" {
			t.Fatalf("codex args do not end with -: %v", args)
		}
		if p.ResumeSessionID != "" && !slices.Equal(args[len(args)-3:], []string{"resume", p.ResumeSessionID, "-"}) {
			t.Fatalf("codex resume args end %v", args[len(args)-3:])
		}
	case "opencode":
		want := []string{"run", "--format", "json", "--auto", "--pure", "--agent", "build", "--model", p.Model}
		if p.ReadOnly {
			want[6] = "plan"
		}
		if p.ResumeSessionID != "" && !p.HardenedRuntime {
			want = append(want, "--session", p.ResumeSessionID, "--fork")
		}
		if !slices.Equal(args, want) {
			t.Fatalf("opencode args = %v, want %v", args, want)
		}
	default:
		t.Fatalf("unknown adapter %s", adapter)
	}
}
