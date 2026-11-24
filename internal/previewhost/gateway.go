package previewhost

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	gatewayAlias         = "our-gateway"
	appAlias             = "app"
	gatewayIngressPort   = 8080
	gatewayEgressPort    = 3128
	gatewayBinary        = "our-preview-gateway"
	gatewayImageRepo     = "our-preview-gateway"
	gatewayImageLabel    = "com.ourplatform.preview-gateway"
	gatewayUser          = "65534:65534"
	gatewayMemoryMB      = 64
	gatewayMemoryLimit   = "48MiB"
	gatewayCPUs          = 0.25
	gatewayPidsLimit     = 64
	minimumDockerMajor   = 26
	staticRunnerRequired = "preview hosting needs a statically linked runner (CGO_ENABLED=0)"
	dockerTooOld         = "preview hosting needs Docker Engine 26 or newer"
)

var reservedServiceNames = []string{appAlias, gatewayAlias}

var proxyEnvKeys = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

type validationError struct {
	err error
}

func (failure validationError) Error() string {
	return failure.err.Error()
}

func (failure validationError) Unwrap() error {
	return failure.err
}

func (d *Docker) checkDockerVersion(ctx context.Context) error {
	d.versionMu.Lock()
	defer d.versionMu.Unlock()
	if d.versionChecked {
		return nil
	}
	version, err := d.engine.ServerVersion(ctx)
	if err != nil {
		return fmt.Errorf("read docker server version: %w", err)
	}
	majorText, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < minimumDockerMajor {
		return validationError{errors.New(dockerTooOld)}
	}
	d.versionChecked = true
	return nil
}

func checkReservedServiceNames(content v1.PreviewBlueprintContentV1) error {
	for _, service := range content.Services {
		if slices.Contains(reservedServiceNames, service.Name) {
			return fmt.Errorf("service name %q is reserved by the preview host", service.Name)
		}
	}
	return nil
}

func hostMachine() (elf.Machine, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return elf.EM_X86_64, true
	case "arm64":
		return elf.EM_AARCH64, true
	}
	return 0, false
}

func checkStaticExecutable(file io.ReaderAt) error {
	parsed, err := elf.NewFile(file)
	if err != nil {
		return errors.New(staticRunnerRequired)
	}
	machine, known := hostMachine()
	if !known || parsed.Machine != machine {
		return errors.New(staticRunnerRequired)
	}
	for _, program := range parsed.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New(staticRunnerRequired)
		}
	}
	needed, err := parsed.ImportedLibraries()
	if err != nil || len(needed) > 0 {
		return errors.New(staticRunnerRequired)
	}
	return nil
}

func (d *Docker) gatewayExecutable() (string, error) {
	if d.cfg.GatewayExecutable != "" {
		return d.cfg.GatewayExecutable, nil
	}
	return d.selfExecutable()
}

func defaultSelfExecutable() (string, error) {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe", nil
	}
	return os.Executable()
}

func (d *Docker) ensureGatewayImage(ctx context.Context) (string, error) {
	executable, err := d.gatewayExecutable()
	if err != nil {
		return "", fmt.Errorf("locate runner executable: %w", err)
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", fmt.Errorf("open runner executable: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect runner executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("runner executable is not a regular file")
	}
	size := info.Size()
	if err := checkStaticExecutable(file); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(file, 0, size)); err != nil {
		return "", fmt.Errorf("hash runner executable: %w", err)
	}
	tag := gatewayImageRepo + ":" + hex.EncodeToString(digest.Sum(nil))[:16]
	present, err := d.engine.ImagePresent(ctx, tag)
	if err != nil {
		return "", err
	}
	if present {
		return tag, nil
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := writeGatewayTar(writer, io.NewSectionReader(file, 0, size), size)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	changes := []string{
		`ENTRYPOINT ["/` + gatewayBinary + `","preview-gateway"]`,
		"LABEL " + gatewayImageLabel + "=1",
	}
	importErr := d.engine.ImportImage(ctx, tag, reader, changes)
	_ = reader.CloseWithError(errors.New("gateway image import finished"))
	writeErr := <-done
	if importErr != nil {
		return "", fmt.Errorf("import gateway image: %w", importErr)
	}
	if writeErr != nil {
		return "", fmt.Errorf("write gateway image: %w", writeErr)
	}
	return tag, nil
}

func writeGatewayTar(writer io.Writer, executable io.Reader, size int64) error {
	archive := tar.NewWriter(writer)
	header := &tar.Header{
		Typeflag: tar.TypeReg, Name: gatewayBinary, Mode: 0o555, Size: size,
		ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
	}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.CopyN(archive, executable, size); err != nil {
		return err
	}
	return archive.Close()
}

func gatewayName(prefix string) string {
	return prefix + "-gateway"
}

func edgeName(prefix string) string {
	return prefix + "-edge"
}

func egressAllowed(content v1.PreviewBlueprintContentV1) bool {
	return content.Egress.Mode == v1.PreviewBlueprintEgressAllow
}

func (d *Docker) gatewaySpec(run *deployment, image string) ContainerSpec {
	content := run.request.Content
	command := []string{
		"--ingress-listen", ":" + strconv.Itoa(gatewayIngressPort),
		"--ingress-target", net.JoinHostPort(appAlias, strconv.Itoa(content.App.Port)),
	}
	if egressAllowed(content) {
		command = append(command, "--egress-listen", ":"+strconv.Itoa(gatewayEgressPort))
		for _, host := range content.Egress.Hosts {
			command = append(command, "--allow", host)
		}
	}
	return ContainerSpec{
		Name: gatewayName(run.prefix), Image: image, Network: edgeName(run.prefix), Labels: run.labels,
		User: gatewayUser, ReadOnly: true, MemoryMB: gatewayMemoryMB, CPUs: gatewayCPUs,
		PidsLimit: gatewayPidsLimit, PublishPort: gatewayIngressPort, Command: command,
		Env: map[string]string{"GOMEMLIMIT": gatewayMemoryLimit},
	}
}

func (d *Docker) startGateway(ctx context.Context, run *deployment) error {
	edge := NetworkSpec{
		Name: edgeName(run.prefix), Labels: run.labels,
		Options: map[string]string{"com.docker.network.enable_ipv6": "false"},
	}
	if err := d.engine.CreateNetwork(ctx, edge); err != nil {
		return err
	}
	image, err := d.ensureGatewayImage(ctx)
	if err != nil {
		return err
	}
	spec := d.gatewaySpec(run, image)
	if err := d.engine.RunDetached(ctx, spec); err != nil {
		return fmt.Errorf("start preview gateway: %w", err)
	}
	if err := d.engine.ConnectNetwork(ctx, run.prefix, spec.Name, gatewayAlias); err != nil {
		return fmt.Errorf("attach preview gateway: %w", err)
	}
	return d.waitForGateway(ctx, spec.Name)
}

func (d *Docker) waitForGateway(ctx context.Context, container string) error {
	for {
		if d.gatewayReady(ctx, container) {
			return nil
		}
		timer := time.NewTimer(d.cfg.HealthInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("preview gateway is not ready: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (d *Docker) gatewayReady(ctx context.Context, container string) bool {
	running, err := d.engine.Running(ctx, container)
	if err != nil || !running {
		return false
	}
	port, err := d.engine.HostPort(ctx, container, gatewayIngressPort)
	if err != nil {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := d.dial(dialCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func proxyEnvironment(content v1.PreviewBlueprintContentV1) map[string]string {
	if !egressAllowed(content) {
		return nil
	}
	proxy := "http://" + net.JoinHostPort(gatewayAlias, strconv.Itoa(gatewayEgressPort))
	env := make(map[string]string, len(proxyEnvKeys)+2)
	for _, key := range proxyEnvKeys {
		env[key] = proxy
	}
	names := make([]string, 0, len(content.Services))
	for _, service := range content.Services {
		names = append(names, service.Name)
	}
	slices.Sort(names)
	noProxy := strings.Join(append([]string{"localhost", "127.0.0.1", gatewayAlias}, names...), ",")
	env["NO_PROXY"] = noProxy
	env["no_proxy"] = noProxy
	return env
}

func applyProxyEnvironment(env map[string]string, content v1.PreviewBlueprintContentV1) {
	maps.Copy(env, proxyEnvironment(content))
}
