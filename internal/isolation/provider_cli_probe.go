package isolation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	providerCLIProbeScript    = `"$0" --version && printf "%s\n" "----" && "$0" --help`
	providerCLIProbeSeparator = "\n----\n"
	maxProviderCLIProbeOutput = 64 << 10
	maxProviderCLIVersion     = 128
)

type ProviderCLIProbe struct {
	Version string
	Help    string
}

type ProviderCLIProber struct {
	Run func(context.Context, []string) ([]byte, error)
}

func ProbeProviderCLI(ctx context.Context, image, binary, runnerID string, mapping UserMapping) (ProviderCLIProbe, error) {
	return ProviderCLIProber{}.Probe(ctx, image, binary, runnerID, mapping)
}

func (p ProviderCLIProber) Probe(ctx context.Context, image, binary, runnerID string, mapping UserMapping) (ProviderCLIProbe, error) {
	if err := mapping.Validate(); err != nil {
		return ProviderCLIProbe{}, err
	}
	if err := validateContainerImage(image); err != nil {
		return ProviderCLIProbe{}, err
	}
	if !runnerLabelPattern.MatchString(runnerID) {
		return ProviderCLIProbe{}, fmt.Errorf("provider CLI probe requires a valid runner label")
	}
	if err := validateProviderBinary(binary); err != nil {
		return ProviderCLIProbe{}, err
	}
	args := []string{
		"run", "--rm", "--user", mapping.User(), "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "1g", "--label", "com.ourplatform.runner-id=" + runnerID, image,
		"sh", "-c", providerCLIProbeScript, binary,
	}
	run := p.Run
	if run == nil {
		run = runProviderCLIProbe
	}
	output, err := run(ctx, args)
	if err != nil {
		return ProviderCLIProbe{}, fmt.Errorf("probe %s in the job image: %w", binary, err)
	}
	return parseProviderCLIProbe(string(output))
}

func validateProviderBinary(binary string) error {
	invalid := fmt.Errorf("provider binary must be a bare command name or an absolute path without whitespace or control characters")
	if binary == "" || strings.HasPrefix(binary, "-") {
		return invalid
	}
	if strings.IndexFunc(binary, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError }) >= 0 {
		return invalid
	}
	if strings.Contains(binary, "/") && !filepath.IsAbs(binary) {
		return invalid
	}
	return nil
}

func parseProviderCLIProbe(output string) (ProviderCLIProbe, error) {
	versionText, help, found := strings.Cut(output, providerCLIProbeSeparator)
	if !found {
		return ProviderCLIProbe{}, fmt.Errorf("the provider CLI probe output is malformed")
	}
	firstLine, _, _ := strings.Cut(versionText, "\n")
	version := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(firstLine, ""))
	version = truncateRunes(strings.TrimSpace(version), maxProviderCLIVersion)
	if version == "" {
		return ProviderCLIProbe{}, fmt.Errorf("the provider CLI reported no version")
	}
	return ProviderCLIProbe{Version: version, Help: help}, nil
}

func truncateRunes(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	cutoff := maximum
	for cutoff > 0 && !utf8.RuneStart(value[cutoff]) {
		cutoff--
	}
	return value[:cutoff]
}

type boundedProbeOutput struct {
	data     []byte
	exceeded bool
}

func (b *boundedProbeOutput) Write(p []byte) (int, error) {
	remaining := maxProviderCLIProbeOutput - len(b.data)
	if len(p) > remaining {
		b.exceeded = true
		if remaining > 0 {
			b.data = append(b.data, p[:remaining]...)
		}
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

var errProviderCLIProbeOutputTooLarge = errors.New("provider CLI probe output exceeds 64 KiB")

func runProviderCLIProbe(ctx context.Context, args []string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// #nosec G204 -- executable is Docker, flags are fixed, and the image, runner label and binary are validated argv items.
	cmd := exec.CommandContext(probeCtx, "docker", args...)
	cmd.Env = DockerEnvironment()
	var stdout boundedProbeOutput
	var stderr cappedProbeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, errProviderCLIProbeOutputTooLarge
	}
	return stdout.data, nil
}
