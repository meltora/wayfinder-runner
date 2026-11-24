package previewhost

import (
	"archive/tar"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var regexp16 = regexp.MustCompile(`^our-preview-gateway:[0-9a-f]{16}$`)

type elfFixture struct {
	machine elf.Machine
	interp  bool
	needed  bool
}

func buildELF(t *testing.T, fixture elfFixture) []byte {
	t.Helper()
	const (
		headerSize  = 64
		programSize = 56
		sectionSize = 64
	)
	type section struct {
		name   string
		kind   elf.SectionType
		link   uint32
		entry  uint64
		data   []byte
		offset uint64
	}
	var sections []section
	if fixture.needed {
		dynamic := &bytes.Buffer{}
		for _, entry := range [][2]uint64{{uint64(elf.DT_NEEDED), 1}, {uint64(elf.DT_NULL), 0}} {
			_ = binary.Write(dynamic, binary.LittleEndian, entry)
		}
		sections = []section{
			{},
			{name: ".dynstr", kind: elf.SHT_STRTAB, data: []byte("\x00libc.so.6\x00")},
			{name: ".dynamic", kind: elf.SHT_DYNAMIC, link: 1, entry: 16, data: dynamic.Bytes()},
			{name: ".shstrtab", kind: elf.SHT_STRTAB},
		}
	}
	names := []byte{0}
	nameOffsets := make([]uint32, len(sections))
	for index := 1; index < len(sections); index++ {
		nameOffsets[index] = uint32(len(names))
		names = append(append(names, sections[index].name...), 0)
	}
	if len(sections) > 0 {
		sections[len(sections)-1].data = names
	}
	interp := []byte("/lib64/ld-linux-x86-64.so.2\x00")
	programs := 1
	if fixture.interp {
		programs = 2
	}
	offset := uint64(headerSize + programs*programSize)
	interpOffset := offset
	if fixture.interp {
		offset += uint64(len(interp))
	}
	for index := 1; index < len(sections); index++ {
		sections[index].offset = offset
		offset += uint64(len(sections[index].data))
	}
	offset = (offset + 7) &^ 7
	sectionOffset := offset
	file := &bytes.Buffer{}
	header := elf.Header64{
		Type: uint16(elf.ET_EXEC), Machine: uint16(fixture.machine), Version: uint32(elf.EV_CURRENT),
		Phoff: headerSize, Ehsize: headerSize, Phentsize: programSize, Phnum: uint16(programs),
		Shentsize: sectionSize,
	}
	copy(header.Ident[:], elf.ELFMAG)
	header.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	if len(sections) > 0 {
		header.Shoff = sectionOffset
		header.Shnum = uint16(len(sections))
		header.Shstrndx = uint16(len(sections) - 1)
	}
	_ = binary.Write(file, binary.LittleEndian, header)
	_ = binary.Write(file, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R | elf.PF_X), Align: 4096})
	if fixture.interp {
		_ = binary.Write(file, binary.LittleEndian, elf.Prog64{
			Type: uint32(elf.PT_INTERP), Flags: uint32(elf.PF_R), Off: interpOffset,
			Filesz: uint64(len(interp)), Memsz: uint64(len(interp)), Align: 1,
		})
		file.Write(interp)
	}
	for index := 1; index < len(sections); index++ {
		file.Write(sections[index].data)
	}
	for uint64(file.Len()) < sectionOffset {
		file.WriteByte(0)
	}
	for index, entry := range sections {
		_ = binary.Write(file, binary.LittleEndian, elf.Section64{
			Name: nameOffsets[index], Type: uint32(entry.kind), Off: entry.offset, Size: uint64(len(entry.data)),
			Link: entry.link, Addralign: 1, Entsize: entry.entry,
		})
	}
	return file.Bytes()
}

func writeExecutable(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "our-runner")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func staticRunnerFixture(t *testing.T) string {
	t.Helper()
	machine, known := hostMachine()
	if !known {
		t.Skip("preview hosting supports amd64 and arm64 runner hosts only")
	}
	return writeExecutable(t, buildELF(t, elfFixture{machine: machine}))
}

func deployedHarness(t *testing.T, content v1.PreviewBlueprintContentV1) *harness {
	t.Helper()
	h := newHarness(t)
	if outcome := h.deploy(context.Background(), content, testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	return h
}

func allowContent() v1.PreviewBlueprintContentV1 {
	content := testContent()
	content.Egress = v1.PreviewBlueprintEgressV1{
		Mode: v1.PreviewBlueprintEgressAllow, Hosts: []string{"registry.npmjs.org", "api.github.com"},
	}
	content.App.Env["HTTPS_PROXY"] = fake("http://blueprint-proxy.example.com:8080")
	content.App.Env["no_proxy"] = fake("*")
	return content
}

func TestPreviewNetworksAreInternalAndEdge(t *testing.T) {
	h := deployedHarness(t, testContent())
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	networks := h.engine.CreatedNetworks()
	if len(networks) != 2 {
		t.Fatalf("networks = %+v", networks)
	}
	internal, edge := networks[0], networks[1]
	if internal.Name != prefix || !internal.Internal ||
		!maps.Equal(internal.Options, map[string]string{
			"com.docker.network.bridge.inhibit_ipv4": "true",
			"com.docker.network.enable_ipv6":         "false",
		}) {
		t.Fatalf("preview network = %+v", internal)
	}
	if edge.Name != prefix+"-edge" || edge.Internal ||
		!maps.Equal(edge.Options, map[string]string{"com.docker.network.enable_ipv6": "false"}) ||
		!maps.Equal(edge.Labels, PreviewLabels(testRunnerID, testPreviewID)) {
		t.Fatalf("edge network = %+v", edge)
	}
	for _, spec := range h.engine.Created() {
		want := prefix
		if spec.Name == prefix+"-gateway" {
			want = prefix + "-edge"
		}
		if spec.Network != want {
			t.Fatalf("container %s on network %q, want %q", spec.Name, spec.Network, want)
		}
	}
}

func TestGatewayContainerPolicyAndArguments(t *testing.T) {
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	for name, test := range map[string]struct {
		content v1.PreviewBlueprintContentV1
		command []string
	}{
		"none": {testContent(), []string{"--ingress-listen", ":8080", "--ingress-target", "app:3000"}},
		"allow": {allowContent(), []string{
			"--ingress-listen", ":8080", "--ingress-target", "app:3000", "--egress-listen", ":3128",
			"--allow", "registry.npmjs.org", "--allow", "api.github.com",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			h := deployedHarness(t, test.content)
			gateway := specNamed(t, h.engine.Created(), prefix+"-gateway")
			if gateway.User != "65534:65534" || !gateway.ReadOnly || len(gateway.CapAdd) != 0 || gateway.PidsLimit != 64 ||
				gateway.MemoryMB != 64 || gateway.CPUs != 0.25 || gateway.PublishPort != 8080 || gateway.Entrypoint != "" ||
				!maps.Equal(gateway.Env, map[string]string{"GOMEMLIMIT": "48MiB"}) ||
				len(gateway.Mounts) != 0 || gateway.NetworkAlias != "" ||
				!maps.Equal(gateway.Labels, PreviewLabels(testRunnerID, testPreviewID)) ||
				!strings.HasPrefix(gateway.Image, "our-preview-gateway:") {
				t.Fatalf("gateway = %+v", gateway)
			}
			if !slices.Equal(gateway.Command, test.command) {
				t.Fatalf("gateway command = %q", gateway.Command)
			}
			if connects := h.engine.Connects(); !slices.Equal(connects, []fakeConnect{{network: prefix, container: prefix + "-gateway", alias: "our-gateway"}}) {
				t.Fatalf("connects = %+v", connects)
			}
			app := specNamed(t, h.engine.Created(), prefix+"-app")
			if app.PublishPort != 0 || app.NetworkAlias != "app" || app.Network != prefix {
				t.Fatalf("app = %+v", app)
			}
		})
	}
}

func TestGatewayStartsBeforeServices(t *testing.T) {
	h := deployedHarness(t, testContent())
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	events := h.engine.Events()
	order := []string{
		"network " + prefix, "network " + prefix + "-edge", "import our-preview-gateway:",
		"detached " + prefix + "-gateway", "connect " + prefix + " " + prefix + "-gateway our-gateway",
		"detached " + prefix + "-svc-db",
	}
	previous := -1
	for _, event := range order {
		index := phaseIndex(t, events, event)
		if index <= previous {
			t.Fatalf("event %q out of order in %v", event, events)
		}
		previous = index
	}
}

func TestProxyEnvironmentOnlyInAllowMode(t *testing.T) {
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	t.Run("allow", func(t *testing.T) {
		h := deployedHarness(t, allowContent())
		wantNoProxy := "localhost,127.0.0.1,our-gateway,cache,db,mail,search"
		appContainers := 0
		for _, spec := range h.engine.Created() {
			if strings.HasPrefix(spec.Name, prefix+"-svc-") || spec.Name == prefix+"-gateway" {
				if _, set := spec.Env["HTTP_PROXY"]; set {
					t.Fatalf("%s carries proxy env", spec.Name)
				}
				continue
			}
			appContainers++
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
				if spec.Env[key] != "http://our-gateway:3128" {
					t.Fatalf("%s %s = %q", spec.Name, key, spec.Env[key])
				}
			}
			if spec.Env["NO_PROXY"] != wantNoProxy || spec.Env["no_proxy"] != wantNoProxy {
				t.Fatalf("%s NO_PROXY = %q no_proxy = %q", spec.Name, spec.Env["NO_PROXY"], spec.Env["no_proxy"])
			}
			if spec.Env["FEATURE_FLAG"] != "on" {
				t.Fatalf("%s lost blueprint env: %v", spec.Name, spec.Env)
			}
		}
		if appContainers != 5 {
			t.Fatalf("app containers with proxy env = %d", appContainers)
		}
	})
	t.Run("none", func(t *testing.T) {
		h := deployedHarness(t, testContent())
		for _, spec := range h.engine.Created() {
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
				if _, set := spec.Env[key]; set {
					t.Fatalf("%s carries %s in egress mode none", spec.Name, key)
				}
			}
		}
	})
}

func TestHealthProbeReadsTheGatewayHostPort(t *testing.T) {
	h := deployedHarness(t, testContent())
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	calls := h.engine.HostPortCalls()
	if len(calls) < 2 {
		t.Fatalf("host port lookups = %v", calls)
	}
	for _, call := range calls {
		if call != prefix+"-gateway:8080" {
			t.Fatalf("host port lookups = %v", calls)
		}
	}
	for _, spec := range h.engine.Created() {
		if spec.PublishPort != 0 && spec.Name != prefix+"-gateway" {
			t.Fatalf("%s publishes port %d", spec.Name, spec.PublishPort)
		}
	}
}

func TestDeployRefusesADynamicallyLinkedRunner(t *testing.T) {
	machine, known := hostMachine()
	if !known {
		t.Skip("preview hosting supports amd64 and arm64 runner hosts only")
	}
	other := elf.EM_AARCH64
	if machine == elf.EM_AARCH64 {
		other = elf.EM_X86_64
	}
	for name, data := range map[string][]byte{
		"interpreter":  buildELF(t, elfFixture{machine: machine, interp: true}),
		"needed":       buildELF(t, elfFixture{machine: machine, needed: true}),
		"foreign arch": buildELF(t, elfFixture{machine: other}),
		"not elf":      []byte("#!/bin/sh\nexit 0\n"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.GatewayExecutable = writeExecutable(t, data)
			outcome := h.deploy(context.Background(), testContent(), testOperation())
			assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseServices)
			if !strings.Contains(outcome.LogTail, "preview hosting needs a statically linked runner (CGO_ENABLED=0)") {
				t.Fatalf("log tail = %q", outcome.LogTail)
			}
			if len(h.engine.Imports()) != 0 {
				t.Fatal("a dynamically linked runner was imported")
			}
			for _, event := range h.engine.Events() {
				if strings.HasPrefix(event, "detached ") {
					t.Fatalf("container started after refusal: %v", h.engine.Events())
				}
			}
		})
	}
	parsed, err := elf.NewFile(bytes.NewReader(buildELF(t, elfFixture{machine: machine, needed: true})))
	if err != nil {
		t.Fatal(err)
	}
	if needed, err := parsed.ImportedLibraries(); err != nil || !slices.Equal(needed, []string{"libc.so.6"}) {
		t.Fatalf("fixture needed = %v err = %v", needed, err)
	}
}

func TestDeployRefusesDockerOlderThan26(t *testing.T) {
	for _, version := range []string{"25.0.5", "20.10.24", "garbage", ""} {
		t.Run(version, func(t *testing.T) {
			h := newHarness(t)
			h.engine.version = version
			outcome := h.deploy(context.Background(), testContent(), testOperation())
			assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseValidate)
			if !strings.Contains(outcome.LogTail, "preview hosting needs Docker Engine 26 or newer") {
				t.Fatalf("log tail = %q", outcome.LogTail)
			}
			if len(h.workspaces.calls) != 0 || len(h.engine.CreatedNetworks()) != 0 {
				t.Fatal("deploy continued past the docker version gate")
			}
		})
	}
	h := newHarness(t)
	h.engine.versionErr = errors.New("cannot connect to the docker daemon")
	unreachable := h.deploy(context.Background(), testContent(), testOperation())
	assertFailedAndTornDown(t, h, unreachable, v1.PreviewDeployPhaseCheckout)
	if strings.Contains(unreachable.LogTail, "Docker Engine 26") || len(h.workspaces.calls) != 0 {
		t.Fatalf("unreachable daemon was reported as too old or continued: %+v", unreachable)
	}
	h.engine.versionErr = nil
	h.engine.version = "26.0.0"
	for attempt := 0; attempt < 2; attempt++ {
		if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
			t.Fatalf("attempt %d outcome = %+v", attempt, outcome)
		}
	}
	if h.engine.versions != 2 {
		t.Fatalf("docker version was read %d times, want once after the failure and once to succeed", h.engine.versions)
	}
}

func TestDeployRefusesReservedServiceNames(t *testing.T) {
	for _, name := range []string{"app", "our-gateway"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			content := testContent()
			content.Services[3].Name = name
			outcome := h.deploy(context.Background(), content, testOperation())
			assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseValidate)
			if !strings.Contains(outcome.LogTail, "reserved") {
				t.Fatalf("log tail = %q", outcome.LogTail)
			}
		})
	}
}

func TestGatewayExecutableDefaultsToTheRunningProcess(t *testing.T) {
	if runtime.GOOS == "linux" {
		if path, err := defaultSelfExecutable(); err != nil || path != "/proc/self/exe" {
			t.Fatalf("default self executable = %q %v", path, err)
		}
	}
	h := newHarness(t)
	self := staticRunnerFixture(t)
	want, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.GatewayExecutable = ""
	h.build()
	var asked atomic.Int32
	h.controller.selfExecutable = func() (string, error) {
		asked.Add(1)
		return self, nil
	}
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	imports := h.engine.Imports()
	if asked.Load() == 0 || len(imports) != 1 {
		t.Fatalf("self executable asked %d times, imports = %d", asked.Load(), len(imports))
	}
	archive := tar.NewReader(bytes.NewReader(imports[0].archive))
	if _, err := archive.Next(); err != nil {
		t.Fatal(err)
	}
	if body, err := io.ReadAll(archive); err != nil || !bytes.Equal(body, want) {
		t.Fatalf("archive body differs from the running process image: %v", err)
	}
}

func TestGatewayAttachFailureTearsEverythingDown(t *testing.T) {
	h := newHarness(t)
	h.engine.connectErr = errors.New("network connect refused")
	outcome := h.deploy(context.Background(), testContent(), testOperation())
	assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseServices)
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	for _, name := range []string{prefix, prefix + "-edge"} {
		if !slices.ContainsFunc(h.engine.CreatedNetworks(), func(spec NetworkSpec) bool { return spec.Name == name }) {
			t.Fatalf("network %s was never created", name)
		}
	}
	if specNamed(t, h.engine.Created(), prefix+"-gateway").Name == "" {
		t.Fatal("gateway was never started")
	}
	if live := h.live(); len(live.Containers) != 0 || len(live.Networks) != 0 {
		t.Fatalf("resources survived the attach failure: %+v", live)
	}
	if slices.ContainsFunc(h.engine.Created(), func(spec ContainerSpec) bool { return spec.Name == prefix+"-app" }) {
		t.Fatal("app started after the gateway failed to attach")
	}
}

func TestGatewayImageIsImportedOnceThenReused(t *testing.T) {
	h := newHarness(t)
	executable, err := os.ReadFile(h.cfg.GatewayExecutable)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
			t.Fatalf("attempt %d outcome = %+v", attempt, outcome)
		}
	}
	imports := h.engine.Imports()
	if len(imports) != 1 {
		t.Fatalf("imports = %d", len(imports))
	}
	imported := imports[0]
	if !regexp16.MatchString(imported.tag) {
		t.Fatalf("tag = %q", imported.tag)
	}
	if !slices.Equal(imported.changes, []string{`ENTRYPOINT ["/our-preview-gateway","preview-gateway"]`, "LABEL com.ourplatform.preview-gateway=1"}) {
		t.Fatalf("changes = %q", imported.changes)
	}
	archive := tar.NewReader(bytes.NewReader(imported.archive))
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "our-preview-gateway" || header.Typeflag != tar.TypeReg || header.Mode != 0o555 || header.Uid != 0 {
		t.Fatalf("header = %+v", header)
	}
	body, err := io.ReadAll(archive)
	if err != nil || !bytes.Equal(body, executable) {
		t.Fatalf("archive body differs from the runner executable: %v", err)
	}
	if _, err := archive.Next(); err != io.EOF {
		t.Fatalf("archive holds more than the gateway binary: %v", err)
	}
	for _, spec := range h.engine.Created() {
		if strings.HasSuffix(spec.Name, "-gateway") && spec.Image != imported.tag {
			t.Fatalf("gateway image = %q, want %q", spec.Image, imported.tag)
		}
	}
	h.cfg.GatewayExecutable = writeExecutable(t, append(buildELF(t, elfFixture{machine: mustHostMachine(t)}), 1))
	h.controller = nil
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if imports := h.engine.Imports(); len(imports) != 2 || imports[1].tag == imported.tag {
		t.Fatalf("a changed runner executable reused the old gateway image: %+v", imports)
	}
}

func mustHostMachine(t *testing.T) elf.Machine {
	t.Helper()
	machine, known := hostMachine()
	if !known {
		t.Skip("preview hosting supports amd64 and arm64 runner hosts only")
	}
	return machine
}

func TestGatewayReadinessWaitsForAConnectableIngress(t *testing.T) {
	h := newHarness(t)
	h.cfg.PhaseTimeouts[v1.PreviewDeployPhaseServices] = 150 * time.Millisecond
	h.engine.hostPort = 1
	outcome := h.deploy(context.Background(), testContent(), testOperation())
	assertFailedAndTornDown(t, h, outcome, v1.PreviewDeployPhaseServices)
	if !strings.Contains(outcome.LogTail, "preview gateway is not ready") {
		t.Fatalf("log tail = %q", outcome.LogTail)
	}
	for _, event := range h.engine.Events() {
		if strings.Contains(event, "-svc-") {
			t.Fatalf("service started before the gateway was ready: %v", h.engine.Events())
		}
	}
}
