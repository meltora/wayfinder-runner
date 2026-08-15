package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/runlock"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/supportbundle"
)

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	var err error
	switch args[0] {
	case "help", "--help", "-h":
		usage()
		return 0
	case "enroll":
		err = cmdEnroll(args[1:])
	case "doctor":
		err = cmdDoctor(args[1:])
	case "start":
		err = cmdStart(args[1:])
	case "status":
		err = cmdStatus(args[1:])
	case "pause":
		err = cmdPause(args[1:])
	case "resume":
		err = cmdResume(args[1:])
	case "mcp-serve":
		err = cmdMCPServe(args[1:])
	case "hook-event":
		err = cmdHookEvent(args[1:])
	case "support-bundle":
		err = cmdSupportBundle(args[1:])
	case "version":
		fmt.Println("our-runner", daemon.Version)
	case "register-repository":
		err = cmdRegisterRepo(args[1:])
	case "scan-agent-config":
		err = cmdScanConfig(args[1:])
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `our-runner — customer-hosted runner for the Autonomous Software Delivery Platform

usage: our-runner <command> [flags]

setup:
  enroll --code-file FILE --url URL [--state-dir DIR]
                                                    one-time enrollment; use FILE=- to read stdin
  doctor --config FILE                             complete production preflight using the service configuration
  doctor --url URL [--state-dir DIR]               limited enrollment/network diagnostics

operation:
  start --config FILE                              start the daemon (outbound-only mTLS connection)
  status [--state-dir DIR]                         show local runner state
  pause [--state-dir DIR]                          pause scheduling (jobs finish, nothing new starts)
  resume [--state-dir DIR]                         resume scheduling after a pause

job tooling:
  register-repository --url REPO [--state-dir DIR] mirror a repository for jobs
  scan-agent-config [--config PATH]                print the agent-config inventory of a repo
  mcp-serve --socket PATH --token-file FILE        stdio MCP bridge (spawned by agent CLIs, not for humans)
  hook-event --socket PATH --token-file FILE --type TYPE
                                                    record non-authoritative provider hook telemetry

maintenance:
  updates are scheduled through the authenticated control plane after the daemon drains active work
  support-bundle [--state-dir DIR]                 redacted diagnostics tarball (no keys, certs, or repo data)
  version                                          print version
  help                                             show this help`)
}

type flags struct {
	stateDir      string
	url           string
	code          string
	codeFile      string
	config        string
	allowInsecure bool
}

func parseFlags(args []string) (flags, error) {
	var f flags
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if name == "--allow-insecure" {
			f.allowInsecure = true
			continue
		}
		if seen[name] {
			return f, fmt.Errorf("flag %s may only be provided once", name)
		}
		seen[name] = true
		if i+1 >= len(args) || args[i+1] == "" {
			return f, fmt.Errorf("flag %s requires a non-empty value", name)
		}
		i++
		value := args[i]
		switch name {
		case "--state-dir":
			f.stateDir = value
		case "--url":
			f.url = value
		case "--code":
			f.code = value
		case "--code-file":
			f.codeFile = value
		case "--config":
			f.config = value
		default:
			return f, fmt.Errorf("unknown flag %s", name)
		}
	}
	if f.stateDir == "" {
		f.stateDir = config.Default().StateDir
	}
	return f, nil
}

func cmdEnroll(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.code != "" {
		return fmt.Errorf("--code is disabled because command-line secrets are visible; use --code-file FILE or --code-file -")
	}
	if f.codeFile == "" || f.url == "" {
		return fmt.Errorf("enroll requires --code-file and --url")
	}
	code, err := readEnrollmentCode(f.codeFile)
	if err != nil {
		return err
	}
	store := identity.NewStore(f.stateDir)
	if store.Enrolled() {
		return fmt.Errorf("already enrolled; use a fresh state dir or clear existing identity")
	}
	key, err := store.EnsureKey()
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	csr, err := identity.NewCSR(key, host)
	if err != nil {
		return err
	}
	client := &enroll.Client{BaseURL: f.url, AllowInsecure: f.allowInsecure}
	resp, err := client.Enroll(context.Background(), enroll.Request{
		EnrollmentCode: code,
		CSR:            string(csr),
		RunnerVersion:  daemon.Version,
	})
	if err != nil {
		return err
	}
	if err := identity.ValidateIssued([]byte(resp.Certificate), []byte(resp.CACert), key, resp.RunnerID, time.Now()); err != nil {
		return fmt.Errorf("validate enrolled identity: %w", err)
	}
	if err := store.SaveIdentity(key, []byte(resp.Certificate), []byte(resp.CACert)); err != nil {
		return err
	}
	if err := daemon.WriteRunnerID(f.stateDir, resp.RunnerID); err != nil {
		return err
	}
	fmt.Println("enrolled as runner", resp.RunnerID)
	return nil
}

func readEnrollmentCode(path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 4097))
	} else {
		raw, err = safeio.ReadRegularFile(path, 4096, 0o077)
	}
	if err != nil {
		return "", fmt.Errorf("read enrollment code: %w", err)
	}
	if len(raw) > 4096 {
		return "", fmt.Errorf("enrollment code is too large")
	}
	code := strings.TrimSpace(string(raw))
	if code == "" {
		return "", fmt.Errorf("enrollment code is empty")
	}
	return code, nil
}

func cmdDoctor(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	opts := doctor.Options{ControlPlaneURL: f.url, StateDir: f.stateDir}
	if f.config != "" {
		if f.url != "" || f.stateDir != config.Default().StateDir {
			return fmt.Errorf("doctor --config cannot be combined with --url or --state-dir")
		}
		cfg, err := config.Load(f.config)
		if err != nil {
			return err
		}
		opts = doctor.Options{
			ControlPlaneURL: cfg.ControlPlaneURL,
			StateDir:        cfg.StateDir,
			Isolation:       cfg.Isolation,
			JobImage:        cfg.JobImage,
			JobNetwork:      cfg.JobNetwork,
			JobNetworkName:  cfg.JobNetworkName,
			EgressPolicyID:  cfg.EgressPolicyID,
		}
	} else if f.url == "" {
		return fmt.Errorf("doctor requires --config FILE or --url URL")
	}
	rep := doctor.Run(context.Background(), opts)
	fmt.Print(doctor.Format(rep))
	if rep.ExitCode() != 0 {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

func cmdStart(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return err
	}
	d, err := daemon.New(cfg, slog.Default())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}

func cmdStatus(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	store := identity.NewStore(f.stateDir)
	fmt.Println("state dir:", f.stateDir)
	fmt.Println("enrolled:", store.Enrolled())
	runnerID, err := daemon.ReadRunnerID(f.stateDir)
	if err == nil {
		fmt.Println("runner id:", runnerID)
	}
	return nil
}

func cmdPause(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := daemon.SetPausedState(f.stateDir, true); err != nil {
		return err
	}
	fmt.Println("paused")
	return nil
}

func cmdResume(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := daemon.SetPausedState(f.stateDir, false); err != nil {
		return err
	}
	fmt.Println("resumed")
	return nil
}

func cmdMCPServe(args []string) error {
	values, err := parseInternalFlags(args, "--socket", "--token-file")
	if err != nil {
		if strings.Contains(err.Error(), "--token") {
			return fmt.Errorf("--token is disabled because command-line secrets are visible; use --token-file")
		}
		return err
	}
	socketPath, tokenFile := values["--socket"], values["--token-file"]
	if socketPath == "" || tokenFile == "" {
		return fmt.Errorf("mcp-serve requires --socket and --token-file")
	}
	token, err := readPrivateTokenFile(tokenFile)
	if err != nil {
		return err
	}
	client := &mcpbridge.UnixClient{SocketPath: socketPath, Token: token}
	srv := &mcpbridge.Server{
		Caller: client.Call,
		ListTools: func(ctx context.Context) ([]mcpbridge.ToolDef, error) {
			discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			result, err := client.Call(discoveryCtx, mcpbridge.InternalListToolsTool, json.RawMessage(`{}`))
			if err != nil {
				return nil, err
			}
			names, err := mcpbridge.ToolNamesFromResult(result)
			if err != nil {
				return nil, err
			}
			return mcpbridge.ToolDefinitionsForNames(names)
		},
	}
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}

func readPrivateTokenFile(tokenFile string) (string, error) {
	raw, err := safeio.ReadRegularFile(tokenFile, 4096, 0o077)
	if err != nil {
		return "", fmt.Errorf("read private MCP token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("MCP token file is empty")
	}
	return token, nil
}

func cmdHookEvent(args []string) error {
	values, err := parseInternalFlags(args, "--socket", "--token-file", "--type")
	if err != nil {
		return err
	}
	socketPath, tokenFile, eventType := values["--socket"], values["--token-file"], values["--type"]
	if socketPath == "" || tokenFile == "" || eventType == "" {
		return fmt.Errorf("hook-event requires --socket, --token-file, and --type")
	}
	if err := mcpbridge.ValidateHookEventType(eventType); err != nil {
		return err
	}
	token, err := readPrivateTokenFile(tokenFile)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (256<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 256<<10 {
		return fmt.Errorf("hook payload exceeds 256 KiB")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("create hook event id: %w", err)
	}
	argsJSON, err := json.Marshal(map[string]any{
		"event_type":      eventType,
		"idempotency_key": "hook-" + hex.EncodeToString(nonce),
	})
	if err != nil {
		return err
	}
	client := &mcpbridge.UnixClient{SocketPath: socketPath, Token: token}
	_, err = client.Call(context.Background(), mcpbridge.InternalHookEventTool, argsJSON)
	return err
}

func parseInternalFlags(args []string, allowed ...string) (map[string]string, error) {
	allow := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allow[name] = true
	}
	values := make(map[string]string, len(allowed))
	for i := 0; i < len(args); i++ {
		name := args[i]
		if !allow[name] {
			return nil, fmt.Errorf("unknown flag %s", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("flag %s may only be provided once", name)
		}
		if i+1 >= len(args) || args[i+1] == "" {
			return nil, fmt.Errorf("flag %s requires a non-empty value", name)
		}
		i++
		values[name] = args[i]
	}
	return values, nil
}

func cmdSupportBundle(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	out, err := supportbundle.Create(f.stateDir)
	if err != nil {
		return err
	}
	fmt.Println("support bundle written to", out)
	return nil
}

func cmdRegisterRepo(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.url == "" {
		return fmt.Errorf("register-repository requires --url <repo-url>")
	}
	lock, err := runlock.Acquire(f.stateDir)
	if err != nil {
		return fmt.Errorf("repository registration requires the runner daemon to be stopped: %w", err)
	}
	defer func() { _ = lock.Close() }()
	rm := repo.NewManager(f.stateDir + "/repos")
	dir, err := rm.Register(context.Background(), f.url)
	if err != nil {
		return err
	}
	branch, err := rm.DefaultBranch(context.Background(), f.url)
	if err != nil {
		return err
	}
	fmt.Println("registered", f.url)
	fmt.Println("  mirror:", dir)
	fmt.Println("  default branch:", branch)
	return nil
}

func cmdScanConfig(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	target := f.config
	if target == "" {
		var err error
		target, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	inv, err := scanner.Scan(target)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(inv)
}
