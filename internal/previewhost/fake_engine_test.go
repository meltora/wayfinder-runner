package previewhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
)

type fakeEngine struct {
	mu         sync.Mutex
	events     []string
	present    map[string]bool
	pullErr    map[string]error
	networks   map[string]NetworkSpec
	containers map[string]ContainerSpec
	created    []ContainerSpec
	createdNet []NetworkSpec
	runOnce    func(ctx context.Context, spec ContainerSpec) (string, error)
	exec       func(ctx context.Context, container string, command []string) (string, error)
	detached   func(spec ContainerSpec) error
	removeErr  error
	removeHook func()
	leftovers  []string
	sticky     bool
	hostPort   int
	stopped    map[string]bool
	listErr    error
	version    string
	versionErr error
	versions   int
	imports    []fakeImport
	connects   []fakeConnect
	connectErr error
	hostPorts  []string
	subnets    map[string][]netip.Prefix
	subnetErr  error
	endpoints  map[string][]string
	listNetErr error
}

var fakeDefaultSubnet = netip.MustParsePrefix("10.89.0.0/24")

func (engine *fakeEngine) NetworkSubnets(ctx context.Context, name string) ([]netip.Prefix, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("subnets " + name)
	if engine.subnetErr != nil {
		return nil, engine.subnetErr
	}
	if _, exists := engine.networks[name]; !exists {
		return nil, fmt.Errorf("network %s does not exist", name)
	}
	if subnets, set := engine.subnets[name]; set {
		return slices.Clone(subnets), nil
	}
	return []netip.Prefix{fakeDefaultSubnet}, nil
}

func (engine *fakeEngine) NetworkContainers(ctx context.Context, name string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.endpoints[name]), nil
}

func (engine *fakeEngine) DisconnectNetwork(ctx context.Context, network, container string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("disconnect " + network + " " + container + " " + strconv.FormatBool(force))
	engine.endpoints[network] = slices.DeleteFunc(engine.endpoints[network], func(name string) bool { return name == container })
	return nil
}

func (engine *fakeEngine) RemoveNetwork(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("rmnet " + name)
	if len(engine.endpoints[name]) > 0 {
		return fmt.Errorf("network %s has active endpoints", name)
	}
	delete(engine.networks, name)
	delete(engine.endpoints, name)
	return nil
}

func (engine *fakeEngine) ListNetworks(ctx context.Context, labels map[string]string) ([]NetworkSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.listNetErr != nil {
		return nil, engine.listNetErr
	}
	var networks []NetworkSummary
	for _, name := range slices.Sorted(maps.Keys(engine.networks)) {
		if spec := engine.networks[name]; labelsMatch(spec.Labels, labels) {
			networks = append(networks, NetworkSummary{Name: name, Labels: maps.Clone(spec.Labels)})
		}
	}
	return networks, nil
}

func (engine *fakeEngine) Networks() map[string]NetworkSpec {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return maps.Clone(engine.networks)
}

func (engine *fakeEngine) Endpoints(network string) []string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.endpoints[network])
}

func (engine *fakeEngine) HostPortCalls() []string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.hostPorts)
}

type fakeImport struct {
	tag     string
	archive []byte
	changes []string
}

type fakeConnect struct {
	network, container, alias string
}

func (engine *fakeEngine) ServerVersion(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.versions++
	if engine.versionErr != nil {
		return "", engine.versionErr
	}
	return engine.version, nil
}

func (engine *fakeEngine) ImportImage(ctx context.Context, tag string, archive io.Reader, changes []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(archive)
	if err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("import " + tag)
	engine.imports = append(engine.imports, fakeImport{tag: tag, archive: data, changes: slices.Clone(changes)})
	engine.present[tag] = true
	return nil
}

func (engine *fakeEngine) ConnectNetwork(ctx context.Context, network, container, alias string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("connect " + network + " " + container + " " + alias)
	if engine.connectErr != nil {
		return engine.connectErr
	}
	if _, exists := engine.networks[network]; !exists {
		return fmt.Errorf("network %s does not exist", network)
	}
	if _, exists := engine.containers[container]; !exists {
		return fmt.Errorf("container %s does not exist", container)
	}
	engine.connects = append(engine.connects, fakeConnect{network: network, container: container, alias: alias})
	engine.endpoints[network] = append(engine.endpoints[network], container)
	return nil
}

func (engine *fakeEngine) Imports() []fakeImport {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.imports)
}

func (engine *fakeEngine) Connects() []fakeConnect {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.connects)
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		present:    map[string]bool{},
		pullErr:    map[string]error{},
		networks:   map[string]NetworkSpec{},
		containers: map[string]ContainerSpec{},
		stopped:    map[string]bool{},
		subnets:    map[string][]netip.Prefix{},
		endpoints:  map[string][]string{},
		version:    "27.3.1",
	}
}

func (engine *fakeEngine) record(event string) {
	engine.events = append(engine.events, event)
}

func (engine *fakeEngine) Events() []string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.events)
}

func (engine *fakeEngine) Created() []ContainerSpec {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.created)
}

func (engine *fakeEngine) CreatedNetworks() []NetworkSpec {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.createdNet)
}

func (engine *fakeEngine) Leftovers() []string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return slices.Clone(engine.leftovers)
}

func (engine *fakeEngine) ImagePresent(ctx context.Context, image string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.present[image], nil
}

func (engine *fakeEngine) Pull(ctx context.Context, image string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("pull " + image)
	if err := engine.pullErr[image]; err != nil {
		return "pull failed for " + image, err
	}
	engine.present[image] = true
	return "pulled " + image, nil
}

func (engine *fakeEngine) CreateNetwork(ctx context.Context, spec NetworkSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("network " + spec.Name)
	if _, exists := engine.networks[spec.Name]; exists {
		return fmt.Errorf("network %s already exists", spec.Name)
	}
	engine.networks[spec.Name] = spec
	engine.createdNet = append(engine.createdNet, spec)
	return nil
}

func (engine *fakeEngine) RunDetached(ctx context.Context, spec ContainerSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	engine.record("detached " + spec.Name)
	engine.created = append(engine.created, spec)
	hook := engine.detached
	engine.mu.Unlock()
	if hook != nil {
		if err := hook(spec); err != nil {
			return err
		}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if _, exists := engine.containers[spec.Name]; exists {
		return fmt.Errorf("container %s already exists", spec.Name)
	}
	engine.containers[spec.Name] = spec
	return nil
}

func (engine *fakeEngine) RunOnce(ctx context.Context, spec ContainerSpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	engine.mu.Lock()
	engine.record("once " + strings.Join(spec.Command, " "))
	engine.created = append(engine.created, spec)
	if _, exists := engine.containers[spec.Name]; exists {
		engine.mu.Unlock()
		return "", fmt.Errorf("container %s already exists", spec.Name)
	}
	engine.containers[spec.Name] = spec
	hook := engine.runOnce
	engine.mu.Unlock()
	output, err := "ran "+strings.Join(spec.Command, " "), error(nil)
	if hook != nil {
		output, err = hook(ctx, spec)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if ctx.Err() != nil {
		engine.leftovers = append(engine.leftovers, spec.Name)
	} else {
		delete(engine.containers, spec.Name)
	}
	return output, err
}

func (engine *fakeEngine) Exec(ctx context.Context, container string, command []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	engine.mu.Lock()
	engine.record("exec " + container + " " + strings.Join(command, " "))
	hook := engine.exec
	engine.mu.Unlock()
	if hook != nil {
		return hook(ctx, container, command)
	}
	return "ok", nil
}

func (engine *fakeEngine) Running(ctx context.Context, container string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	_, exists := engine.containers[container]
	return exists && !engine.stopped[container], nil
}

func (engine *fakeEngine) HostPort(ctx context.Context, container string, port int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.hostPorts = append(engine.hostPorts, container+":"+strconv.Itoa(port))
	spec, exists := engine.containers[container]
	if !exists || spec.PublishPort != port {
		return 0, errors.New("port is not published")
	}
	return engine.hostPort, nil
}

func (engine *fakeEngine) Logs(ctx context.Context, container string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "logs of " + container, nil
}

func (engine *fakeEngine) List(ctx context.Context, labels map[string]string) (Objects, error) {
	if err := ctx.Err(); err != nil {
		return Objects{}, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.listErr != nil {
		return Objects{}, engine.listErr
	}
	var objects Objects
	for _, name := range slices.Sorted(maps.Keys(engine.containers)) {
		if labelsMatch(engine.containers[name].Labels, labels) {
			objects.Containers = append(objects.Containers, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(engine.networks)) {
		if labelsMatch(engine.networks[name].Labels, labels) {
			objects.Networks = append(objects.Networks, name)
		}
	}
	return objects, nil
}

func (engine *fakeEngine) HostPreviewIDs(ctx context.Context, runnerID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.listErr != nil {
		return nil, engine.listErr
	}
	seen := map[string]bool{}
	for _, spec := range engine.containers {
		if spec.Labels[LabelPreviewHost] == runnerID && spec.Labels[LabelPreviewID] != "" {
			seen[spec.Labels[LabelPreviewID]] = true
		}
	}
	for _, spec := range engine.networks {
		if spec.Labels[LabelPreviewHost] == runnerID && spec.Labels[LabelPreviewID] != "" {
			seen[spec.Labels[LabelPreviewID]] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

func (engine *fakeEngine) Remove(ctx context.Context, objects Objects) error {
	engine.mu.Lock()
	hook := engine.removeHook
	engine.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("remove")
	if engine.removeErr != nil {
		return engine.removeErr
	}
	if engine.sticky {
		return nil
	}
	for _, name := range objects.Containers {
		delete(engine.containers, name)
		for network, members := range engine.endpoints {
			engine.endpoints[network] = slices.DeleteFunc(members, func(member string) bool { return member == name })
		}
	}
	for _, name := range objects.Networks {
		delete(engine.networks, name)
		delete(engine.endpoints, name)
	}
	return nil
}

type fakeWorkspaces struct {
	mu    sync.Mutex
	calls []workspaceCall
	err   error
}

type workspaceCall struct {
	URL, SHA, Dest string
	Credential     bool
}

func (workspaces *fakeWorkspaces) CreateDetachedWorkspaceWithCredential(
	_ context.Context, url, sha, dest string, credential *repo.GitHTTPSCredential,
) error {
	workspaces.mu.Lock()
	defer workspaces.mu.Unlock()
	workspaces.calls = append(workspaces.calls, workspaceCall{URL: url, SHA: sha, Dest: dest, Credential: credential != nil})
	if workspaces.err != nil {
		return workspaces.err
	}
	return os.MkdirAll(dest, 0o700)
}

type fakeOwnership struct {
	mu         sync.Mutex
	handOffs   []string
	reclaims   []string
	reclaimErr error
}

func (ownership *fakeOwnership) HandOff(_ context.Context, dir string) error {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	ownership.handOffs = append(ownership.handOffs, dir)
	return nil
}

func (ownership *fakeOwnership) Reclaim(_ context.Context, dir string) error {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	ownership.reclaims = append(ownership.reclaims, dir)
	return ownership.reclaimErr
}

type fakeRepositories map[string]ResolvedRepository

func (repositories fakeRepositories) ResolveRepository(
	_ context.Context, repositoryID string, _ *v1.ProviderCredential,
) (ResolvedRepository, error) {
	resolved, ok := repositories[repositoryID]
	if !ok {
		return ResolvedRepository{}, fmt.Errorf("repository %s is not registered", repositoryID)
	}
	return resolved, nil
}
