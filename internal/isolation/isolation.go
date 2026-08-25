package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
)

type Policy struct {
	Mode                string
	MemoryMB            int
	CPUs                float64
	PIDs                int
	NetworkNone         bool
	NetworkName         string
	ReadOnlyFS          bool
	ReadOnlyWorkDir     bool
	WorkDir             string
	WorkspaceRoot       string
	AgentPackDir        string
	JobHome             string
	MCPSock             string
	MCPTokenFile        string
	Image               string
	RunnerID            string
	AttemptID           string
	NestedSandbox       bool
	DisableLogs         bool
	Environment         []string
	ManagedConfigMounts []supervisor.ManagedConfigMount
}

const DefaultJobImage = "app-runner-job:latest"

type Isolator interface {
	Name() string
	Available() bool
	Wrap(command string, args []string, p Policy) (string, []string, error)
}

type Direct struct{}

func (d Direct) Name() string    { return "direct" }
func (d Direct) Available() bool { return true }
func (d Direct) Wrap(command string, args []string, p Policy) (string, []string, error) {
	return command, args, nil
}

type Docker struct {
	LookPath func(string) (string, error)
}

func (d Docker) Name() string { return "docker" }
func (d Docker) Available() bool {
	lp := d.LookPath
	if lp == nil {
		lp = exec.LookPath
	}
	_, err := lp("docker")
	return err == nil
}

func (d Docker) Wrap(command string, args []string, p Policy) (string, []string, error) {
	dockerArgs := []string{
		"run", "--rm", "-i", "--init", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--cap-drop", "ALL",
	}
	if p.NestedSandbox {
		// The credential-bearing provider is protected from its own model-run
		// subprocesses by Codex/Claude's bubblewrap sandbox. Rootless Docker is
		// mandatory, and the image strips every setuid bit except bwrap.
		dockerArgs = append(dockerArgs,
			"--cap-add", "SYS_ADMIN", "--cap-add", "SYS_CHROOT",
			"--cap-add", "SETUID", "--cap-add", "SETGID",
			"--security-opt", "seccomp=unconfined",
		)
	} else {
		dockerArgs = append(dockerArgs, "--security-opt", "no-new-privileges")
	}
	if p.RunnerID == "" || p.AttemptID == "" {
		return "", nil, fmt.Errorf("docker isolation requires runner and attempt identity")
	}
	dockerArgs = append(dockerArgs,
		"--name", ContainerName(p.RunnerID, p.AttemptID),
		"--label", "com.ourplatform.runner-id="+p.RunnerID,
		"--label", "com.ourplatform.attempt-id="+p.AttemptID,
	)
	if p.DisableLogs {
		dockerArgs = append(dockerArgs, "--log-driver", "none")
	}
	if p.MemoryMB > 0 {
		dockerArgs = append(dockerArgs, "--memory", strconv.Itoa(p.MemoryMB)+"m")
	}
	if p.CPUs > 0 {
		dockerArgs = append(dockerArgs, "--cpus", strconv.FormatFloat(p.CPUs, 'f', -1, 64))
	}
	if p.PIDs > 0 {
		dockerArgs = append(dockerArgs, "--pids-limit", strconv.Itoa(p.PIDs))
	}
	if p.NetworkNone {
		dockerArgs = append(dockerArgs, "--network", "none")
	} else if p.NetworkName != "" {
		dockerArgs = append(dockerArgs, "--network", p.NetworkName)
	} else {
		return "", nil, fmt.Errorf("docker isolation requires network none or an approved restricted network")
	}
	if p.ReadOnlyFS {
		dockerArgs = append(dockerArgs, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m")
	}
	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = p.WorkDir
	}
	if workspaceRoot != "" {
		mount := workspaceRoot + ":" + workspaceRoot + ":rw"
		if p.ReadOnlyWorkDir {
			mount = workspaceRoot + ":" + workspaceRoot + ":ro"
		}
		dockerArgs = append(dockerArgs, "-v", mount)
		if p.WorkDir != "" {
			dockerArgs = append(dockerArgs, "-w", p.WorkDir)
		}
	}
	if p.AgentPackDir != "" {
		dockerArgs = append(dockerArgs, "-v", p.AgentPackDir+":"+p.AgentPackDir+":ro")
	}
	if p.JobHome != "" {
		dockerArgs = append(dockerArgs, "-v", p.JobHome+":/home/runner:rw", "-e", "HOME=/home/runner")
	}
	if p.MCPSock != "" {
		mcpDir := filepath.Dir(p.MCPSock)
		if p.MCPTokenFile == "" || filepath.Dir(p.MCPTokenFile) == mcpDir {
			dockerArgs = append(dockerArgs, "-v", mcpDir+":"+mcpDir+":ro")
		} else {
			dockerArgs = append(dockerArgs, "-v", p.MCPSock+":"+p.MCPSock+":ro")
			dockerArgs = append(dockerArgs, "-v", p.MCPTokenFile+":"+p.MCPTokenFile+":ro")
		}
		dockerArgs = append(dockerArgs, "-e", "PLATFORM_MCP_SOCKET="+p.MCPSock)
		if p.MCPTokenFile != "" {
			dockerArgs = append(dockerArgs, "-e", "PLATFORM_MCP_TOKEN_FILE="+p.MCPTokenFile)
		}
	}
	if len(p.ManagedConfigMounts) > 4 {
		return "", nil, fmt.Errorf("too many managed provider config mounts")
	}
	for _, mount := range p.ManagedConfigMounts {
		if err := validateManagedConfigMount(mount, p.MCPSock); err != nil {
			return "", nil, err
		}
		dockerArgs = append(dockerArgs, "-v", mount.Source+":"+mount.Target+":ro")
	}
	for _, entry := range p.Environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !containerEnvironmentKey.MatchString(key) || len(entry) > 64<<10 || strings.ContainsAny(entry, "\x00\r\n") {
			return "", nil, fmt.Errorf("docker isolation received an invalid container environment entry")
		}
		dockerArgs = append(dockerArgs, "-e", entry)
	}
	image := p.Image
	if image == "" {
		image = DefaultJobImage
	}
	dockerArgs = append(dockerArgs, image, command)
	dockerArgs = append(dockerArgs, args...)
	return "docker", dockerArgs, nil
}

var containerEnvironmentKey = regexp.MustCompile(`^(CLAUDE_CODE_SUBPROCESS_ENV_SCRUB|OPENCODE_(CONFIG_CONTENT|CONFIG_DIR|DISABLE_[A-Z0-9_]+))$`)

func validateManagedConfigMount(mount supervisor.ManagedConfigMount, mcpSocket string) error {
	if mount.Target != "/etc/codex/requirements.toml" || !filepath.IsAbs(mount.Source) || mcpSocket == "" {
		return fmt.Errorf("managed provider config mount is not allowlisted")
	}
	if filepath.Dir(filepath.Clean(mount.Source)) != filepath.Dir(filepath.Clean(mcpSocket)) || strings.ContainsAny(mount.Source, "\x00\r\n:") {
		return fmt.Errorf("managed provider config source is outside the private job directory")
	}
	if _, err := safeio.ReadRegularFile(mount.Source, 1<<20, 0o077); err != nil {
		return fmt.Errorf("validate managed provider config: %w", err)
	}
	return nil
}

func Select(preferred string, allowUnsafeDirect bool) (Isolator, error) {
	switch preferred {
	case "direct":
		if !allowUnsafeDirect {
			return nil, fmt.Errorf("direct execution is disabled; configure a production isolator")
		}
		return Direct{}, nil
	case "docker":
		d := Docker{}
		if !d.Available() {
			return nil, fmt.Errorf("docker isolation requested but docker not available")
		}
		return d, nil
	}
	return nil, fmt.Errorf("unknown isolation mode %q", preferred)
}
