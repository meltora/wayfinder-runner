package previewhost

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
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
	sticky     bool
	hostPort   int
	stopped    map[string]bool
	listErr    error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		present:    map[string]bool{},
		pullErr:    map[string]error{},
		networks:   map[string]NetworkSpec{},
		containers: map[string]ContainerSpec{},
		stopped:    map[string]bool{},
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

func (engine *fakeEngine) ImagePresent(_ context.Context, image string) (bool, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.present[image], nil
}

func (engine *fakeEngine) Pull(_ context.Context, image string) (string, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.record("pull " + image)
	if err := engine.pullErr[image]; err != nil {
		return "pull failed for " + image, err
	}
	engine.present[image] = true
	return "pulled " + image, nil
}

func (engine *fakeEngine) CreateNetwork(_ context.Context, spec NetworkSpec) error {
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

func (engine *fakeEngine) RunDetached(_ context.Context, spec ContainerSpec) error {
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
	engine.mu.Lock()
	engine.record("once " + strings.Join(spec.Command, " "))
	engine.created = append(engine.created, spec)
	hook := engine.runOnce
	engine.mu.Unlock()
	if hook != nil {
		return hook(ctx, spec)
	}
	return "ran " + strings.Join(spec.Command, " "), nil
}

func (engine *fakeEngine) Exec(ctx context.Context, container string, command []string) (string, error) {
	engine.mu.Lock()
	engine.record("exec " + container + " " + strings.Join(command, " "))
	hook := engine.exec
	engine.mu.Unlock()
	if hook != nil {
		return hook(ctx, container, command)
	}
	return "ok", nil
}

func (engine *fakeEngine) Running(_ context.Context, container string) (bool, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	_, exists := engine.containers[container]
	return exists && !engine.stopped[container], nil
}

func (engine *fakeEngine) HostPort(_ context.Context, container string, port int) (int, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	spec, exists := engine.containers[container]
	if !exists || spec.PublishPort != port {
		return 0, errors.New("port is not published")
	}
	return engine.hostPort, nil
}

func (engine *fakeEngine) Logs(_ context.Context, container string) (string, error) {
	return "logs of " + container, nil
}

func (engine *fakeEngine) List(_ context.Context, labels map[string]string) (Objects, error) {
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

func (engine *fakeEngine) Remove(_ context.Context, objects Objects) error {
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
	}
	for _, name := range objects.Networks {
		delete(engine.networks, name)
	}
	return nil
}

func labelsMatch(have, want map[string]string) bool {
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
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
