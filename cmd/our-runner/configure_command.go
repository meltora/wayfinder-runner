package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/remoteconfig"
)

const configureFlagHelp = "--non-interactive together with --provider-env NAME=VALUE, --provider-binary PATH and --acknowledge-direct as needed"

var providerEnvironmentPrompts = map[string]string{
	"claude": "CLAUDE_CONFIG_DIR",
	"codex":  "CODEX_HOME",
}

var providerDisplayNames = map[string]string{
	"codex":    "Codex",
	"claude":   "Claude Code",
	"opencode": "OpenCode",
}

func providerDisplayName(provider string) string {
	if name, ok := providerDisplayNames[provider]; ok {
		return name
	}
	return provider
}

type configureDependencies struct {
	input    io.Reader
	output   io.Writer
	terminal bool
	newFetch func(stateDir, gatewayURL string) (remoteconfig.FetchFunc, error)
	sleep    func(context.Context, time.Duration) error
}

type configureOptions struct {
	gateway           string
	gatewayFromFile   bool
	stateDir          string
	stateDirSet       bool
	providerEnv       map[string]string
	providerBinary    string
	acknowledgeDirect bool
	nonInteractive    bool
	previewHosting    string
}

func cmdConfigure(args []string) error {
	return cmdConfigureWithDependencies(args, configureDependencies{
		input: os.Stdin, output: os.Stdout, terminal: stdinIsTerminal(), newFetch: newRemoteFetch,
	})
}

func newRemoteFetch(stateDir, gatewayURL string) (remoteconfig.FetchFunc, error) {
	fetcher, err := remoteconfig.NewFetcher(stateDir, gatewayURL)
	if err != nil {
		return nil, err
	}
	return fetcher.Fetch, nil
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func parseConfigureFlags(args []string) (configureOptions, error) {
	opts := configureOptions{providerEnv: map[string]string{}}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		switch name {
		case "--acknowledge-direct":
			opts.acknowledgeDirect = true
			continue
		case "--non-interactive":
			opts.nonInteractive = true
			continue
		case "--gateway", "--state-dir", "--provider-env", "--provider-binary", "--preview-hosting":
		default:
			return opts, fmt.Errorf("unknown flag %s", name)
		}
		if name != "--provider-env" && seen[name] {
			return opts, fmt.Errorf("flag %s may only be provided once", name)
		}
		seen[name] = true
		if i+1 >= len(args) || args[i+1] == "" {
			return opts, fmt.Errorf("flag %s requires a non-empty value", name)
		}
		i++
		value := args[i]
		switch name {
		case "--gateway":
			opts.gateway = value
		case "--state-dir":
			opts.stateDir = value
			opts.stateDirSet = true
		case "--preview-hosting":
			if value != "on" && value != "off" {
				return opts, fmt.Errorf("--preview-hosting must be on or off")
			}
			opts.previewHosting = value
		case "--provider-binary":
			if !filepath.IsAbs(value) {
				return opts, fmt.Errorf("--provider-binary must be an absolute path")
			}
			opts.providerBinary = value
		case "--provider-env":
			variable, setting, ok := strings.Cut(value, "=")
			if !ok || variable == "" || setting == "" {
				return opts, fmt.Errorf("--provider-env must be NAME=VALUE")
			}
			if _, exists := opts.providerEnv[variable]; exists {
				return opts, fmt.Errorf("--provider-env %s may only be provided once", variable)
			}
			opts.providerEnv[variable] = setting
		}
	}
	if opts.gateway != "" {
		if _, err := remoteconfig.ConfigurationURL(opts.gateway); err != nil {
			return opts, fmt.Errorf("--gateway: %w", err)
		}
	}
	if opts.stateDir == "" {
		opts.stateDir = config.Default().StateDir
	}
	absolute, err := filepath.Abs(opts.stateDir)
	if err != nil {
		return opts, err
	}
	opts.stateDir = absolute
	return opts, nil
}

type configurePrompter struct {
	reader *bufio.Reader
	output io.Writer
}

func (p configurePrompter) ask(prompt string) (string, error) {
	fmt.Fprint(p.output, prompt)
	line, err := p.reader.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("configure input ended before all questions were answered")
	}
	return strings.TrimSpace(line), nil
}

func cmdConfigureWithDependencies(args []string, deps configureDependencies) error {
	opts, err := parseConfigureFlags(args)
	if err != nil {
		return err
	}
	if !identity.NewStore(opts.stateDir).Enrolled() {
		return remoteconfig.ErrNotEnrolled
	}
	runnerID, err := daemon.ReadRunnerID(opts.stateDir)
	if err != nil {
		return remoteconfig.ErrNotEnrolled
	}
	if opts.gateway == "" {
		stored, ok, err := loadStoredGatewayURL(opts.stateDir)
		if err != nil {
			return err
		}
		if !ok {
			return errNoKnownGateway
		}
		opts.gateway, opts.gatewayFromFile = stored, true
	}
	interactive := !opts.nonInteractive
	if interactive && !deps.terminal {
		return fmt.Errorf("configure asks for local settings on a terminal; without one, rerun it with %s", configureFlagHelp)
	}
	fetch, err := deps.newFetch(opts.stateDir, opts.gateway)
	if err != nil {
		return err
	}
	policy := remoteconfig.ConfigurePolicy()
	if deps.sleep != nil {
		policy.Sleep = deps.sleep
	}
	policy.OnWaiting = func() {
		fmt.Fprintln(deps.output, "Waiting for the dashboard to save this runner's configuration…")
	}
	raw, err := remoteconfig.FetchWithRetry(context.Background(), fetch, policy)
	if err != nil {
		return err
	}
	delivery, err := remoteconfig.ParseDelivery(raw, runnerID)
	if err != nil {
		return err
	}
	local, err := startingLocal(opts)
	if err != nil {
		return err
	}
	printConfigureSummary(deps.output, delivery)
	prompter := configurePrompter{reader: bufio.NewReader(deps.input), output: deps.output}
	provider := delivery.Remote.Provider
	providerName := providerDisplayName(provider)
	if len(opts.providerEnv) > 0 {
		local.ProviderEnvironment[provider] = opts.providerEnv
	} else if variable, ok := providerEnvironmentPrompts[provider]; ok && interactive {
		hint := "optional; Enter leaves it unset"
		if local.ProviderEnvironment[provider][variable] != "" {
			hint = "currently set; Enter keeps it, - clears it"
		}
		answer, err := prompter.ask(fmt.Sprintf("%s for %s (%s): ", variable, providerName, hint))
		if err != nil {
			return err
		}
		switch answer {
		case "":
		case "-":
			delete(local.ProviderEnvironment, provider)
		default:
			local.ProviderEnvironment[provider] = map[string]string{variable: answer}
		}
	}
	if opts.providerBinary != "" {
		local.ProviderBinaries[provider] = opts.providerBinary
	} else if interactive {
		prompt := fmt.Sprintf("Path to the %s binary (optional; press Enter to find it on PATH): ", providerName)
		if saved := local.ProviderBinaries[provider]; saved != "" {
			if !executableRegularFile(saved) {
				fmt.Fprintf(deps.output, "The saved %s binary %s is not an executable file.\n", providerName, saved)
			}
			prompt = fmt.Sprintf("Path to the %s binary [current: %s] (Enter keeps it, - finds it on PATH): ", providerName, saved)
		}
		answer, err := prompter.ask(prompt)
		if err != nil {
			return err
		}
		switch answer {
		case "":
		case "-":
			delete(local.ProviderBinaries, provider)
		default:
			if !filepath.IsAbs(answer) {
				return fmt.Errorf("the %s binary path must be absolute", providerName)
			}
			local.ProviderBinaries[provider] = answer
		}
	}
	switch opts.previewHosting {
	case "on":
		local.PreviewHosting.Enabled = true
	case "off":
		local.PreviewHosting = config.PreviewHostingConfig{}
	}
	if delivery.Remote.Isolation != "direct" {
		local.AcknowledgeDirect = false
	} else if opts.acknowledgeDirect {
		local.AcknowledgeDirect = true
	}
	if delivery.Remote.Isolation == "direct" && !local.AcknowledgeDirect {
		if !interactive {
			return fmt.Errorf("the dashboard selected direct isolation, which is not sandboxed; rerun with --acknowledge-direct to allow it on this host")
		}
		answer, err := prompter.ask("Type direct to allow jobs to run on this host without a sandbox: ")
		if err != nil {
			return err
		}
		if answer != "direct" {
			return fmt.Errorf("direct isolation was not confirmed; nothing was saved")
		}
		local.AcknowledgeDirect = true
	}
	merged, err := remoteconfig.Merge(local, delivery)
	if err != nil {
		return err
	}
	if err := remoteconfig.SaveFiles(opts.stateDir, local, raw); err != nil {
		return err
	}
	fmt.Fprintf(deps.output, "Saved %s and %s in %s.\n", remoteconfig.LocalFileName, remoteconfig.RemoteFileName, opts.stateDir)
	if names := merged.Config.ProviderEnvironmentNames(provider); len(names) > 0 {
		fmt.Fprintf(deps.output, "Provider environment for %s: %s\n", providerName, strings.Join(names, ", "))
	}
	fmt.Fprintln(deps.output, previewHostingSummary(merged.Config.PreviewHosting))
	fmt.Fprintf(deps.output, "Start the runner with: our-runner start%s\n", stateDirArgument(opts))
	return nil
}

func startingLocal(opts configureOptions) (remoteconfig.Local, error) {
	local := remoteconfig.Local{ProviderEnvironment: map[string]map[string]string{}, ProviderBinaries: map[string]string{}}
	existing, err := remoteconfig.LoadLocal(opts.stateDir)
	if err != nil && !errors.Is(err, remoteconfig.ErrNotConfigured) {
		return local, err
	}
	if err == nil {
		for provider, variables := range existing.ProviderEnvironment {
			local.ProviderEnvironment[provider] = variables
		}
		for provider, binary := range existing.ProviderBinaries {
			local.ProviderBinaries[provider] = binary
		}
		local.AcknowledgeDirect = existing.AcknowledgeDirect
		local.Checks = existing.Checks
		local.PreviewHosting = existing.PreviewHosting
	}
	local.ControlPlaneURL = opts.gateway
	local.StateDir = opts.stateDir
	return local, nil
}

func previewHostingSummary(hosting config.PreviewHostingConfig) string {
	if !hosting.Enabled {
		return "Preview hosting: off (turn it on with our-runner configure --preview-hosting on)"
	}
	return fmt.Sprintf("Preview hosting: on, at most %d previews at once", hosting.MaxConcurrent)
}

func printConfigureSummary(output io.Writer, delivery remoteconfig.Delivery) {
	remote := delivery.Remote
	fmt.Fprintf(output, "Runner %s (configuration revision %d)\n", delivery.RunnerID, delivery.ProfileRevision)
	if remote.Isolation == "direct" {
		fmt.Fprintln(output, "Isolation: direct. Jobs are not sandboxed and run with this user's access to the host.")
	} else {
		fmt.Fprintf(output, "Isolation: docker, job image %s\n", remote.JobImage)
		if remote.JobNetwork == "restricted" {
			fmt.Fprintf(output, "Job network: restricted, Docker network %s, egress policy %s (checked against the network labels at start)\n", remote.JobNetworkName, remote.EgressPolicyID)
		} else {
			fmt.Fprintln(output, "Job network: none (jobs have no network access)")
		}
	}
	fmt.Fprintf(output, "Provider: %s\n", providerDisplayName(remote.Provider))
	fmt.Fprintf(output, "Repositories: %d included, %d omitted%s\n", len(remote.Repositories), len(delivery.OmittedRepositories), omissionBreakdown(delivery.OmittedRepositories))
	if remote.AgentPackTrustedKeySet == nil {
		fmt.Fprintln(output, "Platform trust: no signed key set delivered yet")
		return
	}
	fmt.Fprintln(output, "Platform trust: signed key set delivered; the runner verifies it against this build's trust roots")
}

func omissionBreakdown(omitted []remoteconfig.OmittedRepository) string {
	counts := map[string]int{}
	for _, entry := range omitted {
		counts[entry.Reason]++
	}
	parts := make([]string, 0, 2)
	if count := counts[remoteconfig.OmissionCloneURLUnavailable]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d without a clone URL", count))
	}
	if count := counts[remoteconfig.OmissionInstallationInactive]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d on a suspended or revoked GitHub installation", count))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func executableRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func stateDirArgument(opts configureOptions) string {
	if !opts.stateDirSet {
		return ""
	}
	return " --state-dir " + shellQuote(opts.stateDir)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
