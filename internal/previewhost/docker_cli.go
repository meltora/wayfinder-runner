package previewhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/example/app-runner/internal/isolation"
)

const (
	dockerOutputLimit = 64 << 10
	defaultPidsLimit  = 512
)

var (
	dockerContainerIDPattern    = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	dockerOrphanEndpointPattern = regexp.MustCompile(`^ep-[0-9a-f]{12,64}$`)
	dockerObjectNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type DockerCLI struct {
	TempDir string
}

func (cli DockerCLI) run(ctx context.Context, args ...string) (string, error) {
	return cli.runWithInput(ctx, nil, args...)
}

func (cli DockerCLI) runWithInput(ctx context.Context, input io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = isolation.DockerEnvironment()
	cmd.Stdin = input
	output := &tailBuffer{limit: dockerOutputLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = dockerWaitDelay
	err := cmd.Run()
	return output.String(), err
}

type dockerSoftLine func(lower string) bool

func dockerOnlySoft(output string, soft ...dockerSoftLine) bool {
	return dockerOnlySoftBesides(output, nil, soft...)
}

func dockerOnlySoftBesides(output string, removed []string, soft ...dockerSoftLine) bool {
	matched := false
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" || lower == "[]" || slices.ContainsFunc(removed, func(name string) bool { return strings.ToLower(name) == lower }) {
			continue
		}
		if !slices.ContainsFunc(soft, func(match dockerSoftLine) bool { return match(lower) }) {
			return false
		}
		matched = true
	}
	return matched
}

func anyName(names []string, match func(lower, name string) bool) dockerSoftLine {
	return func(lower string) bool {
		return slices.ContainsFunc(names, func(name string) bool { return match(lower, strings.ToLower(name)) })
	}
}

func networkMissing(names ...string) dockerSoftLine {
	return anyName(names, func(lower, name string) bool {
		return strings.HasSuffix(lower, "network "+name+" not found") || strings.HasSuffix(lower, "no such network: "+name)
	})
}

func containerMissing(names ...string) dockerSoftLine {
	return anyName(names, func(lower, name string) bool {
		return strings.HasSuffix(lower, "no such container: "+name)
	})
}

func endpointMissing(name string) dockerSoftLine {
	return anyName([]string{name}, func(lower, name string) bool {
		return strings.HasSuffix(lower, "endpoint "+name+" not found")
	})
}

func containerNotConnected(lower string) bool {
	return strings.Contains(lower, "is not connected to")
}

func containerRemovalInProgress(ids ...string) dockerSoftLine {
	return anyName(ids, func(lower, id string) bool {
		return strings.HasSuffix(lower, "removal of container "+id+" is already in progress")
	})
}

func imageMissing(lower string) bool {
	return strings.Contains(lower, "no such image: ")
}

func volumeMissing(lower string) bool {
	return strings.HasSuffix(lower, ": no such volume")
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func (cli DockerCLI) ServerVersion(ctx context.Context) (string, error) {
	output, err := cli.run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return "", fmt.Errorf("docker version: %w: %s", err, strings.TrimSpace(output))
	}
	return strings.TrimSpace(output), nil
}

func (cli DockerCLI) ImportImage(ctx context.Context, tag string, archive io.Reader, changes []string) error {
	args := []string{"import"}
	for _, change := range changes {
		args = append(args, "--change", change)
	}
	output, err := cli.runWithInput(ctx, archive, append(args, "-", tag)...)
	if err != nil {
		return fmt.Errorf("import image: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) ConnectNetwork(ctx context.Context, network, container, alias string) error {
	args := []string{"network", "connect"}
	if alias != "" {
		args = append(args, "--alias", alias)
	}
	output, err := cli.run(ctx, append(args, network, container)...)
	if err != nil {
		return fmt.Errorf("connect network: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) NetworkSubnets(ctx context.Context, name string) ([]netip.Prefix, error) {
	output, err := cli.run(ctx, "network", "inspect", "--format", "{{json .IPAM.Config}}", name)
	if err != nil {
		return nil, fmt.Errorf("inspect network subnets: %w: %s", err, strings.TrimSpace(output))
	}
	var config []struct {
		Subnet string
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &config); err != nil {
		return nil, errors.New("docker returned an invalid network IPAM configuration")
	}
	var subnets []netip.Prefix
	for _, entry := range config {
		if entry.Subnet == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(entry.Subnet)
		if err != nil {
			return nil, errors.New("docker returned an invalid network subnet")
		}
		subnets = append(subnets, prefix.Masked())
	}
	return subnets, nil
}

func (cli DockerCLI) NetworkContainers(ctx context.Context, name string) ([]string, error) {
	output, err := cli.run(ctx, "network", "inspect", "--format", "{{range $id, $endpoint := .Containers}}{{$id}} {{end}}", name)
	if err != nil {
		if ctx.Err() == nil && dockerOnlySoft(output, networkMissing(name)) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect network containers: %w: %s", err, strings.TrimSpace(output))
	}
	var ids []string
	for _, id := range strings.Fields(output) {
		if dockerOrphanEndpointPattern.MatchString(id) {
			continue
		}
		if !dockerContainerIDPattern.MatchString(id) {
			return nil, errors.New("docker returned an invalid container identifier")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (cli DockerCLI) DisconnectNetwork(ctx context.Context, network, container string, force bool) error {
	args := []string{"network", "disconnect"}
	if force {
		args = append(args, "--force")
	}
	output, err := cli.run(ctx, append(args, network, container)...)
	soft := []dockerSoftLine{networkMissing(network), containerMissing(container), endpointMissing(container), containerNotConnected}
	if err != nil && (ctx.Err() != nil || !dockerOnlySoft(output, soft...)) {
		return fmt.Errorf("disconnect network: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) RemoveNetwork(ctx context.Context, name string) error {
	output, err := cli.run(ctx, "network", "rm", name)
	if err != nil && (ctx.Err() != nil || !dockerOnlySoft(output, networkMissing(name))) {
		return fmt.Errorf("remove network: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) ListNetworks(ctx context.Context, labels map[string]string) ([]NetworkSummary, error) {
	if len(labels) == 0 {
		return nil, errors.New("network listing requires at least one label")
	}
	for key, value := range labels {
		if key == "" || value == "" {
			return nil, errors.New("network listing requires non-empty labels")
		}
	}
	args := append([]string{"network", "ls", "--no-trunc", "--format", "{{.Name}}"}, labelArgs("--filter", prefixLabels(labels))...)
	output, err := cli.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w: %s", err, strings.TrimSpace(output))
	}
	names := strings.Fields(output)
	if len(names) == 0 {
		return nil, nil
	}
	for _, name := range names {
		if !dockerObjectNamePattern.MatchString(name) {
			return nil, errors.New("docker returned an invalid network name")
		}
	}
	inspectArgs := append([]string{"network", "inspect", "--format", `{{json .Name}}{{"\t"}}{{json .Labels}}`}, names...)
	output, err = cli.run(ctx, inspectArgs...)
	if err != nil {
		return nil, fmt.Errorf("inspect networks: %w: %s", err, strings.TrimSpace(output))
	}
	var networks []NetworkSummary
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		nameJSON, labelsJSON, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found {
			return nil, errors.New("docker returned an invalid network listing")
		}
		var network NetworkSummary
		if json.Unmarshal([]byte(nameJSON), &network.Name) != nil || json.Unmarshal([]byte(labelsJSON), &network.Labels) != nil ||
			!dockerObjectNamePattern.MatchString(network.Name) {
			return nil, errors.New("docker returned an invalid network listing")
		}
		if !labelsMatch(network.Labels, labels) {
			continue
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func (cli DockerCLI) ImagePresent(ctx context.Context, image string) (bool, error) {
	output, err := cli.run(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err == nil {
		return true, nil
	}
	if ctx.Err() == nil && dockerOnlySoft(output, imageMissing) {
		return false, nil
	}
	return false, fmt.Errorf("inspect image: %w", err)
}

func (cli DockerCLI) Pull(ctx context.Context, image string) (string, error) {
	return cli.run(ctx, "pull", "--quiet", image)
}

func (cli DockerCLI) CreateNetwork(ctx context.Context, spec NetworkSpec) error {
	args := []string{"network", "create", "--driver", "bridge", "--ipv6=false"}
	if spec.Internal {
		args = append(args, "--internal")
	}
	args = append(args, labelArgs("-o", spec.Options)...)
	args = append(args, labelArgs("--label", spec.Labels)...)
	output, err := cli.run(ctx, append(args, spec.Name)...)
	if err != nil {
		return fmt.Errorf("create network: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) RunDetached(ctx context.Context, spec ContainerSpec) error {
	output, err := cli.runContainer(ctx, spec, true)
	if err != nil {
		return fmt.Errorf("start container: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (cli DockerCLI) RunOnce(ctx context.Context, spec ContainerSpec) (string, error) {
	return cli.runContainer(ctx, spec, false)
}

func (cli DockerCLI) privateTempDir() (string, error) {
	if cli.TempDir == "" || !filepath.IsAbs(cli.TempDir) {
		return "", errors.New("preview docker engine requires an absolute private temporary directory")
	}
	dir := filepath.Clean(cli.TempDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if resolved != dir {
		return "", errors.New("preview docker temporary directory must not be reached through a symlink")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("preview docker temporary directory must be a 0700 directory")
	}
	if !ownedByCurrentUser(info) {
		return "", errors.New("preview docker temporary directory must be owned by the runner")
	}
	return dir, nil
}

func (cli DockerCLI) runContainer(ctx context.Context, spec ContainerSpec, detached bool) (string, error) {
	dir, err := cli.privateTempDir()
	if err != nil {
		return "", err
	}
	envFile := ""
	if len(spec.Env) > 0 {
		path, err := writeEnvFile(dir, spec.Env)
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(path) }()
		envFile = path
	}
	return cli.run(ctx, runArgs(spec, envFile, detached)...)
}

func writeEnvFile(dir string, env map[string]string) (string, error) {
	var body strings.Builder
	for _, key := range slices.Sorted(maps.Keys(env)) {
		value := env[key]
		if strings.ContainsAny(key, "=\n\r\x00") || strings.ContainsAny(value, "\n\r\x00") {
			return "", errors.New("container environment entry cannot be written to an env file")
		}
		body.WriteString(key + "=" + value + "\n")
	}
	file, err := os.CreateTemp(dir, "preview-env-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(body.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func labelArgs(flag string, labels map[string]string) []string {
	var args []string
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, flag, key+"="+labels[key])
	}
	return args
}

func runArgs(spec ContainerSpec, envFile string, detached bool) []string {
	args := []string{"run"}
	if detached {
		args = append(args, "-d")
	} else {
		args = append(args, "--rm")
	}
	args = append(args, "--name", spec.Name, "--cap-drop", "ALL")
	for _, capability := range spec.CapAdd {
		args = append(args, "--cap-add", capability)
	}
	pids := spec.PidsLimit
	if pids <= 0 {
		pids = defaultPidsLimit
	}
	args = append(args, "--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(pids))
	if spec.MemoryMB > 0 {
		memory := strconv.Itoa(spec.MemoryMB) + "m"
		args = append(args, "--memory", memory, "--memory-swap", memory)
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	args = append(args, "--network", spec.Network)
	if spec.NetworkAlias != "" {
		args = append(args, "--network-alias", spec.NetworkAlias)
	}
	args = append(args, labelArgs("--label", spec.Labels)...)
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.ReadOnly {
		args = append(args, "--read-only")
	}
	for _, tmpfs := range spec.Tmpfs {
		args = append(args, "--tmpfs", tmpfs)
	}
	for _, mount := range spec.Mounts {
		value := "type=bind,source=" + mount.Source + ",target=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	if spec.WorkDir != "" {
		args = append(args, "-w", spec.WorkDir)
	}
	if spec.PublishPort > 0 {
		args = append(args, "-p", "127.0.0.1::"+strconv.Itoa(spec.PublishPort))
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	args = append(args, spec.Image)
	return append(args, spec.Command...)
}

func (cli DockerCLI) Exec(ctx context.Context, container string, command []string) (string, error) {
	return cli.run(ctx, append([]string{"exec", container}, command...)...)
}

func (cli DockerCLI) Running(ctx context.Context, container string) (bool, error) {
	output, err := cli.run(ctx, "container", "inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		if ctx.Err() == nil && dockerOnlySoft(output, containerMissing(container)) {
			return false, nil
		}
		return false, fmt.Errorf("inspect container: %w", err)
	}
	return strings.TrimSpace(output) == "true", nil
}

func (cli DockerCLI) HostPort(ctx context.Context, container string, port int) (int, error) {
	output, err := cli.run(ctx, "port", container, strconv.Itoa(port)+"/tcp")
	if err != nil {
		return 0, fmt.Errorf("inspect published port: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		host, portText, err := net.SplitHostPort(strings.TrimSpace(line))
		if err != nil || host != "127.0.0.1" {
			continue
		}
		hostPort, err := strconv.Atoi(portText)
		if err == nil && hostPort > 0 && hostPort <= 65535 {
			return hostPort, nil
		}
	}
	return 0, errors.New("container port is not published on 127.0.0.1")
}

func (cli DockerCLI) Logs(ctx context.Context, container string) (string, error) {
	return cli.run(ctx, "logs", "--tail", "200", container)
}

func (cli DockerCLI) List(ctx context.Context, labels map[string]string) (Objects, error) {
	if labels[LabelPreviewHost] == "" || labels[LabelPreviewID] == "" {
		return Objects{}, errors.New("preview resource listing requires both preview labels")
	}
	filters := labelArgs("--filter", prefixLabels(labels))
	var objects Objects
	var err error
	if objects.Containers, err = cli.listIDs(ctx, dockerContainerIDPattern, append([]string{"ps", "-aq", "--no-trunc"}, filters...)); err != nil {
		return Objects{}, err
	}
	if objects.Networks, err = cli.listIDs(ctx, dockerObjectNamePattern, append([]string{"network", "ls", "-q", "--no-trunc"}, filters...)); err != nil {
		return Objects{}, err
	}
	if objects.Volumes, err = cli.listIDs(ctx, dockerObjectNamePattern, append([]string{"volume", "ls", "-q"}, filters...)); err != nil {
		return Objects{}, err
	}
	return objects, nil
}

func prefixLabels(labels map[string]string) map[string]string {
	filters := make(map[string]string, len(labels))
	for key, value := range labels {
		filters["label="+key] = value
	}
	return filters
}

func (cli DockerCLI) listIDs(ctx context.Context, pattern *regexp.Regexp, args []string) ([]string, error) {
	output, err := cli.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list preview resources: %w", err)
	}
	ids := strings.Fields(output)
	for _, id := range ids {
		if !pattern.MatchString(id) {
			return nil, errors.New("docker returned an invalid object identifier")
		}
	}
	return ids, nil
}

func (cli DockerCLI) HostPreviewIDs(ctx context.Context, runnerID string) ([]string, error) {
	if runnerID == "" {
		return nil, errors.New("preview host listing requires a runner id")
	}
	filter := []string{"--filter", "label=" + LabelPreviewHost + "=" + runnerID, "--format", `{{.Label "` + LabelPreviewID + `"}}`}
	seen := map[string]bool{}
	var ids []string
	for _, command := range [][]string{{"ps", "-a"}, {"network", "ls"}, {"volume", "ls"}} {
		output, err := cli.run(ctx, append(command, filter...)...)
		if err != nil {
			return nil, fmt.Errorf("list preview host resources: %w", err)
		}
		for _, line := range strings.Split(output, "\n") {
			id := strings.TrimSpace(line)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (cli DockerCLI) Remove(ctx context.Context, objects Objects) error {
	steps := []struct {
		args []string
		ids  []string
		soft []dockerSoftLine
	}{
		{[]string{"rm", "-f", "-v"}, objects.Containers, []dockerSoftLine{
			containerMissing(objects.Containers...), containerRemovalInProgress(objects.Containers...),
		}},
		{[]string{"network", "rm"}, objects.Networks, []dockerSoftLine{networkMissing(objects.Networks...)}},
		{[]string{"volume", "rm", "-f"}, objects.Volumes, []dockerSoftLine{volumeMissing}},
	}
	for _, step := range steps {
		if len(step.ids) == 0 {
			continue
		}
		output, err := cli.run(ctx, append(step.args, step.ids...)...)
		if err != nil && (ctx.Err() != nil || !dockerOnlySoftBesides(output, step.ids, step.soft...)) {
			return fmt.Errorf("remove preview resources: %w: %s", err, strings.TrimSpace(output))
		}
	}
	return nil
}

type tailBuffer struct {
	limit int
	data  []byte
}

func (buffer *tailBuffer) Write(p []byte) (int, error) {
	buffer.data = append(buffer.data, p...)
	if excess := len(buffer.data) - buffer.limit; excess > 0 {
		buffer.data = append([]byte(nil), buffer.data[excess:]...)
	}
	return len(p), nil
}

func (buffer *tailBuffer) String() string {
	return string(buffer.data)
}
