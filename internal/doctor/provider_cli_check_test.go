package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/isolation"
)

type doctorProbeCall struct {
	image, binary, runnerID string
	mapping                 isolation.UserMapping
}

func helpFixture(t *testing.T, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "provider", "testdata", "claude-help-"+version+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func withProviderCLIProbe(t *testing.T, probe func(binary string) (isolation.ProviderCLIProbe, error)) *[]doctorProbeCall {
	t.Helper()
	var calls []doctorProbeCall
	original := probeProviderCLI
	probeProviderCLI = func(_ context.Context, image, binary, runnerID string, mapping isolation.UserMapping) (isolation.ProviderCLIProbe, error) {
		calls = append(calls, doctorProbeCall{image: image, binary: binary, runnerID: runnerID, mapping: mapping})
		return probe(binary)
	}
	t.Cleanup(func() { probeProviderCLI = original })
	return &calls
}

func dockerClaudeOptions() Options {
	return Options{Isolation: "docker", JobImage: "registry/job@sha256:test", Provider: "claude", ConfigurationLoaded: true}
}

func TestProviderCLICheckStatuses(t *testing.T) {
	compatible := func(string) (isolation.ProviderCLIProbe, error) {
		return isolation.ProviderCLIProbe{Version: "2.1.299 (Claude Code)", Help: helpFixture(t, "2.1.299")}, nil
	}
	for name, test := range map[string]struct {
		opts       Options
		mappingErr error
		probe      func(string) (isolation.ProviderCLIProbe, error)
		status     Status
		detail     string
		probes     int
	}{
		"no configuration": {
			opts: Options{}, status: Warn,
			detail: "provider CLI in the job image not tested; run doctor --config FILE before production use",
		},
		"no image": {
			opts: Options{Isolation: "docker", Provider: "claude"}, status: Warn,
			detail: "provider CLI in the job image not tested; run doctor --config FILE before production use",
		},
		"no provider": {
			opts: Options{Isolation: "docker", JobImage: "registry/job@sha256:test"}, status: Warn,
			detail: "provider CLI in the job image not tested; run doctor --config FILE before production use",
		},
		"direct isolation": {
			opts: Options{Isolation: "direct", JobImage: "registry/job@sha256:test", Provider: "claude"}, status: Warn,
			detail: "direct isolation is development-only",
		},
		"mapping error": {
			opts: dockerClaudeOptions(), mappingErr: errors.New("the daemon's gid map does not match its uid map"), status: Fail,
			detail: "job user mapping: the daemon's gid map does not match its uid map",
		},
		"provider without a contract": {
			opts: Options{Isolation: "docker", JobImage: "registry/job@sha256:test", Provider: "codex"}, status: Warn,
			detail: "no runner option contract is declared for codex; its CLI options were not probed",
		},
		"probe error": {
			opts: dockerClaudeOptions(), status: Fail, probes: 1,
			probe: func(string) (isolation.ProviderCLIProbe, error) {
				return isolation.ProviderCLIProbe{}, errors.New("probe claude in the job image: exit status 127")
			},
			detail: "probe claude in the job image: exit status 127",
		},
		"missing options": {
			opts: dockerClaudeOptions(), status: Fail, probes: 1,
			probe: func(string) (isolation.ProviderCLIProbe, error) {
				return isolation.ProviderCLIProbe{Version: "2.1.221 (Claude Code)", Help: helpFixture(t, "2.1.221")}, nil
			},
			detail: "2.1.221 (Claude Code) in the job image does not accept the runner options --permission-prompts, --restricted",
		},
		"all present": {
			opts: dockerClaudeOptions(), status: OK, probes: 1, probe: compatible,
			detail: "2.1.299 (Claude Code) in the job image accepts all 21 runner options",
		},
	} {
		t.Run(name, func(t *testing.T) {
			probe := test.probe
			if probe == nil {
				probe = func(string) (isolation.ProviderCLIProbe, error) {
					t.Fatal("probe ran")
					return isolation.ProviderCLIProbe{}, nil
				}
			}
			calls := withProviderCLIProbe(t, probe)
			c := checkProviderCLI(context.Background(), test.opts, testRootlessMapping, test.mappingErr)
			if c.Name != "job-image-provider-cli" || c.Status != test.status || c.Detail != test.detail {
				t.Fatalf("check = %+v\nwant status %v detail %q", c, test.status, test.detail)
			}
			if len(*calls) != test.probes {
				t.Fatalf("probe calls = %+v", *calls)
			}
			if test.probes == 1 {
				call := (*calls)[0]
				if call.image != "registry/job@sha256:test" || call.binary != "claude" || call.runnerID != "doctor" || call.mapping != testRootlessMapping {
					t.Fatalf("probe call = %+v", call)
				}
			}
		})
	}
}

func TestProviderCLICheckUsesTheConfiguredBinaries(t *testing.T) {
	calls := withProviderCLIProbe(t, func(string) (isolation.ProviderCLIProbe, error) {
		return isolation.ProviderCLIProbe{Version: "2.1.299 (Claude Code)", Help: helpFixture(t, "2.1.299")}, nil
	})
	opts := dockerClaudeOptions()
	opts.ProviderBinary = "/opt/tools/claude"
	if c := checkProviderCLI(context.Background(), opts, testRootlessMapping, nil); c.Status != OK {
		t.Fatalf("check = %+v", c)
	}
	if len(*calls) != 1 || (*calls)[0].binary != "/opt/tools/claude" {
		t.Fatalf("probe calls = %+v", *calls)
	}
}

func TestProviderCLICheckProbesTheProposalAdapterAndReportsTheWorstStatus(t *testing.T) {
	calls := withProviderCLIProbe(t, func(string) (isolation.ProviderCLIProbe, error) {
		return isolation.ProviderCLIProbe{Version: "2.1.221 (Claude Code)", Help: helpFixture(t, "2.1.221")}, nil
	})
	opts := Options{
		Isolation: "docker", JobImage: "registry/job@sha256:test", Provider: "codex",
		WorkIntakeProposalEnabled: true, WorkIntakeProposalAdapter: "claude", WorkIntakeProposalBinary: "/opt/claude",
	}
	c := checkProviderCLI(context.Background(), opts, testRootlessMapping, nil)
	want := "codex: no runner option contract is declared for codex; its CLI options were not probed; claude: 2.1.221 (Claude Code) in the job image does not accept the runner options --permission-prompts, --restricted"
	if c.Status != Fail || c.Detail != want {
		t.Fatalf("check = %+v\nwant %q", c, want)
	}
	if len(*calls) != 1 || (*calls)[0].binary != "/opt/claude" {
		t.Fatalf("probe calls = %+v", *calls)
	}
	opts.Provider = "claude"
	opts.ProviderBinary = "/opt/claude"
	*calls = nil
	c = checkProviderCLI(context.Background(), opts, testRootlessMapping, nil)
	if len(*calls) != 1 || strings.HasPrefix(c.Detail, "claude: ") {
		t.Fatalf("shared provider probed %d times: %+v", len(*calls), c)
	}
	opts.WorkIntakeProposalEnabled = false
	opts.Provider = "codex"
	*calls = nil
	c = checkProviderCLI(context.Background(), opts, testRootlessMapping, nil)
	if len(*calls) != 0 || c.Status != Warn {
		t.Fatalf("disabled proposal adapter probed: %+v %+v", *calls, c)
	}
}
