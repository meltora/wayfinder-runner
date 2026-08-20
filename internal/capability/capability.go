package capability

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
)

type Detector struct {
	RunnerID                   string
	RunnerVersion              string
	Labels                     map[string]string
	LookPath                   func(string) (string, error)
	RunVersion                 func(ctx context.Context, name string, args ...string) (string, error)
	ProbeDocker                func(ctx context.Context) isolation.DockerInfo
	SourcePublicationProviders []string
	PreviewObservationProfiles []v1.PreviewObservationProfileCapability
	trustedAgentPackKeyIDs     []string
}

func New(runnerID, version string, labels, agentPackPublicKeys map[string]string) *Detector {
	trustedAgentPackKeyIDs := make([]string, 0, len(agentPackPublicKeys))
	for keyID := range agentPackPublicKeys {
		trustedAgentPackKeyIDs = append(trustedAgentPackKeyIDs, keyID)
	}
	sort.Strings(trustedAgentPackKeyIDs)
	return &Detector{
		RunnerID:               runnerID,
		RunnerVersion:          version,
		Labels:                 labels,
		LookPath:               exec.LookPath,
		RunVersion:             defaultRunVersion,
		ProbeDocker:            isolation.ProbeDocker,
		trustedAgentPackKeyIDs: trustedAgentPackKeyIDs,
	}
}

func defaultRunVersion(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// #nosec G204 -- callers use a fixed allowlist of git/docker/provider version commands without a shell.
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (d *Detector) Collect(ctx context.Context) v1.CapabilityManifest {
	sourcePublicationProviders := uniqueSorted(d.SourcePublicationProviders)
	previewObservationProfiles := clonePreviewObservationProfiles(d.PreviewObservationProfiles)
	m := v1.CapabilityManifest{
		RunnerID:                          d.RunnerID,
		CollectedAt:                       time.Now().UTC(),
		OS:                                runtime.GOOS,
		Arch:                              runtime.GOARCH,
		CPUs:                              runtime.NumCPU(),
		MemoryBytes:                       totalMemory(),
		Labels:                            d.Labels,
		RunnerVersion:                     d.RunnerVersion,
		ProtocolVersion:                   v1.ProtocolVersion,
		AgentPackDeliveryAuthorityVersion: v1.AgentPackDeliveryAuthorityVersion,
		TrustedAgentPackKeyIDs:            append([]string(nil), d.trustedAgentPackKeyIDs...),
		SourcePublicationProviders:        sourcePublicationProviders,
		PreviewObservationProfiles:        previewObservationProfiles,
	}
	if len(sourcePublicationProviders) != 0 {
		m.SourcePublicationTransportVersion = v1.SourcePublicationTransportVersion
	}
	if len(previewObservationProfiles) != 0 {
		m.PreviewObservationTransportVersion = v1.PreviewObservationTransportVersion
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(".", &fs); err == nil {
		m.DiskFreeBytes = fs.Bavail * uint64(fs.Bsize)
	}
	if _, err := d.LookPath("git"); err == nil {
		if out, err := d.RunVersion(ctx, "git", "--version"); err == nil {
			m.GitVersion = out
		}
	}
	for _, eng := range []string{"docker", "podman"} {
		if _, err := d.LookPath(eng); err == nil {
			m.ContainerEngine = eng
			break
		}
	}
	if m.ContainerEngine == "docker" {
		if d.ProbeDocker != nil {
			info := d.ProbeDocker(ctx)
			m.ContainerRootless = info.Rootless || info.UsernsRemap
		}
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		m.KVM = true
	}
	for _, agent := range []struct{ adapter, bin string }{
		{"codex", "codex"},
		{"claude", "claude"},
		{"opencode", "opencode"},
	} {
		ac := v1.AgentCapability{Provider: agent.adapter}
		if adapter, err := provider.For(agent.adapter); err == nil {
			hooks := adapter.ManagedHooks()
			ac.ManagedHooksSupported = hooks.Supported
			ac.ManagedHooksVersion = hooks.Version
			ac.ManagedHookEvents = append([]string(nil), hooks.Events...)
		}
		if _, err := d.LookPath(agent.bin); err == nil {
			ac.Available = true
			if out, err := d.RunVersion(ctx, agent.bin, "--version"); err == nil {
				ac.Version = out
			}
			ac.AuthReady = authReady(agent.adapter)
		}
		m.Agents = append(m.Agents, ac)
	}
	for _, sup := range []string{"herdr", "tmux"} {
		if _, err := d.LookPath(sup); err == nil {
			m.Supervisors = append(m.Supervisors, sup)
		}
	}
	return m
}

func clonePreviewObservationProfiles(
	values []v1.PreviewObservationProfileCapability,
) []v1.PreviewObservationProfileCapability {
	result := make([]v1.PreviewObservationProfileCapability, len(values))
	for index, value := range values {
		value.HealthCheckIDs = append([]string(nil), value.HealthCheckIDs...)
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].BlueprintID != result[right].BlueprintID {
			return result[left].BlueprintID < result[right].BlueprintID
		}
		if result[left].Version != result[right].Version {
			return result[left].Version < result[right].Version
		}
		return result[left].Digest < result[right].Digest
	})
	return result
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func authReady(provider string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	switch provider {
	case "codex":
		_, err := os.Stat(home + "/.codex/auth.json")
		return err == nil
	case "claude":
		if _, err := os.Stat(home + "/.claude/.credentials.json"); err == nil {
			return true
		}
		_, err := os.Stat(home + "/.claude.json")
		return err == nil
	case "opencode":
		_, err := os.Stat(home + "/.local/share/opencode/auth.json")
		return err == nil
	}
	return false
}

func totalMemory() uint64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err == nil {
		var mem uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &mem); err == nil {
			return mem
		}
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var mem uint64
	if _, err := fmt.Sscanf(string(data), "MemTotal: %d kB", &mem); err != nil {
		return 0
	}
	return mem * 1024
}

func (d *Detector) Sign(m *v1.CapabilityManifest, key *ecdsa.PrivateKey) error {
	m.Signature = ""
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return err
	}
	m.Signature = base64.StdEncoding.EncodeToString(sig)
	return nil
}
