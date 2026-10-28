package isolation

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testProbeImage = "registry.example.test/job@sha256:abc"

func recordingProbeRun(output string, err error, calls *[][]string) func(context.Context, []string) ([]byte, error) {
	return func(_ context.Context, args []string) ([]byte, error) {
		*calls = append(*calls, append([]string(nil), args...))
		return []byte(output), err
	}
}

func TestProviderCLIProbeRunsOneHardenedContainerAsTheJobUser(t *testing.T) {
	var calls [][]string
	prober := ProviderCLIProber{Run: recordingProbeRun("2.1.299 (Claude Code)\n----\nUsage: claude\n  --restricted  x\n", nil, &calls)}
	result, err := prober.Probe(context.Background(), testProbeImage, "claude", "runner-1", testRootlessMapping)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--rm", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "1g", "--label", "com.ourplatform.runner-id=runner-1", testProbeImage,
		"sh", "-c", `"$0" --version && printf "%s\n" "----" && "$0" --help`, "claude",
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("docker calls = %q\nwant %q", calls, want)
	}
	if result.Version != "2.1.299 (Claude Code)" || result.Help != "Usage: claude\n  --restricted  x\n" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProviderCLIProbePassesAnAbsoluteBinaryAsAPositionalArgument(t *testing.T) {
	var calls [][]string
	prober := ProviderCLIProber{Run: recordingProbeRun("1.0\n----\n", nil, &calls)}
	if _, err := prober.Probe(context.Background(), testProbeImage, "/opt/tools/claude", "doctor", testIdentityMapping); err != nil {
		t.Fatal(err)
	}
	args := calls[0]
	if args[len(args)-1] != "/opt/tools/claude" || strings.Contains(args[len(args)-2], "/opt/tools/claude") || args[3] != "1000:1000" {
		t.Fatalf("args = %q", args)
	}
}

func TestProviderCLIProbeRefusesInvalidInputsBeforeRunningDocker(t *testing.T) {
	for name, test := range map[string]struct {
		image, binary, runner string
		mapping               UserMapping
		want                  string
	}{
		"unresolved mapping":  {testProbeImage, "claude", "runner-1", UserMapping{}, "has not been resolved"},
		"invalid image":       {"-bad", "claude", "runner-1", testRootlessMapping, "valid job image reference"},
		"empty image":         {"", "claude", "runner-1", testRootlessMapping, "valid job image reference"},
		"invalid runner":      {testProbeImage, "claude", "runner 1", testRootlessMapping, "valid runner label"},
		"empty binary":        {testProbeImage, "", "runner-1", testRootlessMapping, "provider binary"},
		"option binary":       {testProbeImage, "--help", "runner-1", testRootlessMapping, "provider binary"},
		"whitespace binary":   {testProbeImage, "claude x", "runner-1", testRootlessMapping, "provider binary"},
		"tab binary":          {testProbeImage, "claude\t", "runner-1", testRootlessMapping, "provider binary"},
		"control binary":      {testProbeImage, "claude\x00", "runner-1", testRootlessMapping, "provider binary"},
		"newline binary":      {testProbeImage, "claude\n", "runner-1", testRootlessMapping, "provider binary"},
		"relative path":       {testProbeImage, "bin/claude", "runner-1", testRootlessMapping, "provider binary"},
		"dot relative path":   {testProbeImage, "./claude", "runner-1", testRootlessMapping, "provider binary"},
		"unicode space":       {testProbeImage, "claude ", "runner-1", testRootlessMapping, "provider binary"},
		"delete control char": {testProbeImage, "claude\x7f", "runner-1", testRootlessMapping, "provider binary"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			prober := ProviderCLIProber{Run: recordingProbeRun("1\n----\n", nil, &calls)}
			_, err := prober.Probe(context.Background(), test.image, test.binary, test.runner, test.mapping)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if len(calls) != 0 {
				t.Fatalf("docker ran: %q", calls)
			}
		})
	}
}

func TestProviderCLIProbeParsesAndBoundsTheVersionLine(t *testing.T) {
	long := strings.Repeat("é", 100)
	for name, test := range map[string]struct {
		output, version, help, err string
	}{
		"trimmed":            {"  2.1.299 (Claude Code)  \r\nextra\n----\nhelp", "2.1.299 (Claude Code)", "help", ""},
		"control characters": {"2.1\x1b[31m.299\x00\n----\n", "2.1[31m.299", "", ""},
		"rune boundary":      {long + "\n----\n", strings.Repeat("é", 64), "", ""},
		"missing separator":  {"2.1.299 (Claude Code)\nUsage", "", "", "malformed"},
		"empty version":      {"\n----\nUsage", "", "", "no version"},
		"control only":       {"\x01\x02\n----\nUsage", "", "", "no version"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			prober := ProviderCLIProber{Run: recordingProbeRun(test.output, nil, &calls)}
			result, err := prober.Probe(context.Background(), testProbeImage, "claude", "runner-1", testRootlessMapping)
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("err = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Version != test.version || result.Help != test.help {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestProviderCLIProbeWrapsARunFailure(t *testing.T) {
	var calls [][]string
	prober := ProviderCLIProber{Run: recordingProbeRun("", errors.New("exit status 127: claude: not found"), &calls)}
	_, err := prober.Probe(context.Background(), testProbeImage, "claude", "runner-1", testRootlessMapping)
	if err == nil || !strings.Contains(err.Error(), "probe claude in the job image") || !strings.Contains(err.Error(), "claude: not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestProviderCLIProbeDefaultRunnerCapsOutputAndReportsStderr(t *testing.T) {
	installFakeDocker(t, `
case "$*" in
  *runner-ok*) printf '2.1.299 (Claude Code)\n----\nUsage: claude\n' ;;
  *runner-big*) i=0; while [ $i -lt 70 ]; do head -c 1024 /dev/zero | tr '\0' 'x'; i=$((i+1)); done ;;
  *runner-err*) i=0; while [ $i -lt 40 ]; do head -c 1024 /dev/zero | tr '\0' 'e' >&2; i=$((i+1)); done; exit 3 ;;
esac`)
	t.Setenv("PATH", os.Getenv("PATH")+":/usr/bin:/bin")
	result, err := ProbeProviderCLI(context.Background(), testProbeImage, "claude", "runner-ok", testRootlessMapping)
	if err != nil || result.Version != "2.1.299 (Claude Code)" || result.Help != "Usage: claude\n" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if _, err := ProbeProviderCLI(context.Background(), testProbeImage, "claude", "runner-big", testRootlessMapping); err == nil || !strings.Contains(err.Error(), "exceeds 64 KiB") {
		t.Fatalf("oversized output err = %v", err)
	}
	_, err = ProbeProviderCLI(context.Background(), testProbeImage, "claude", "runner-err", testRootlessMapping)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "eeee") || len(err.Error()) > 33<<10 {
		t.Fatalf("stderr err length %d: %.200v", len(err.Error()), err)
	}
}
