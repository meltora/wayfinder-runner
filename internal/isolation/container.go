package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var dockerNetworkIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DockerNetworkAttestation binds a configured network label to Docker's
// immutable object identity. It deliberately does not claim that mutable host
// firewall/proxy enforcement is effective; consultation separately requires a
// fresh root-owned egress-policy attestation.
type DockerNetworkAttestation struct {
	ID       string
	Name     string
	PolicyID string
	Driver   string
	Scope    string
}

func ContainerName(runnerID, attemptID string) string {
	digest := sha256.Sum256([]byte(runnerID + "\x00" + attemptID))
	return "our-runner-" + hex.EncodeToString(digest[:12])
}

func RemoveJobContainer(ctx context.Context, runnerID, attemptID string) error {
	return removeContainers(ctx, []string{ContainerName(runnerID, attemptID)})
}

func RemoveRunnerContainers(ctx context.Context, runnerID string) error {
	if runnerID == "" {
		return fmt.Errorf("runner ID is required for container cleanup")
	}
	// #nosec G204 -- executable/flags are fixed and runnerID is a validated label value.
	cmd := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+runnerID)
	cmd.Env = dockerEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list runner containers: %w", err)
	}
	ids := strings.Fields(string(out))
	for _, id := range ids {
		if len(id) < 12 || len(id) > 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return fmt.Errorf("docker returned an invalid container ID")
		}
	}
	return removeContainers(ctx, ids)
}

func VerifyDockerNetwork(ctx context.Context, networkName, policyID string) error {
	_, err := AttestDockerNetwork(ctx, networkName, policyID)
	return err
}

func AttestDockerNetwork(ctx context.Context, networkName, policyID string) (DockerNetworkAttestation, error) {
	if networkName == "" || policyID == "" {
		return DockerNetworkAttestation{}, fmt.Errorf("network name and policy ID are required")
	}
	// #nosec G204 -- executable/flags are fixed and networkName is validated configuration, passed as one argv item.
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", "--format", "{{json .}}", networkName)
	cmd.Env = dockerEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return DockerNetworkAttestation{}, fmt.Errorf("inspect restricted Docker network: %w", err)
	}
	var inspected struct {
		ID         string            `json:"Id"`
		Name       string            `json:"Name"`
		Driver     string            `json:"Driver"`
		Scope      string            `json:"Scope"`
		Internal   bool              `json:"Internal"`
		Attachable bool              `json:"Attachable"`
		Ingress    bool              `json:"Ingress"`
		Labels     map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(out, &inspected); err != nil {
		return DockerNetworkAttestation{}, fmt.Errorf("decode Docker network attestation: %w", err)
	}
	if !dockerNetworkIDPattern.MatchString(inspected.ID) || inspected.Name != networkName ||
		inspected.Driver != "bridge" || inspected.Scope != "local" || inspected.Internal || inspected.Attachable || inspected.Ingress {
		return DockerNetworkAttestation{}, fmt.Errorf("Docker network topology is not eligible for restricted consultation")
	}
	if inspected.Labels["com.ourplatform.egress-enforced"] != "true" || inspected.Labels["com.ourplatform.egress-policy"] != policyID {
		return DockerNetworkAttestation{}, fmt.Errorf("Docker network is not bound to enforced egress policy %q", policyID)
	}
	return DockerNetworkAttestation{
		ID: inspected.ID, Name: inspected.Name, PolicyID: policyID, Driver: inspected.Driver, Scope: inspected.Scope,
	}, nil
}

func VerifyDockerNetworkAttestation(ctx context.Context, expected DockerNetworkAttestation) error {
	current, err := AttestDockerNetwork(ctx, expected.Name, expected.PolicyID)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("restricted Docker network identity changed")
	}
	return nil
}

func removeContainers(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, ids...)
	// #nosec G204 -- executable/flags are fixed and container IDs are strict lowercase hexadecimal values.
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("remove job container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dockerEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"DOCKER_HOST": true, "DOCKER_CONTEXT": true, "XDG_RUNTIME_DIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	}
	var out []string
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}

func DockerEnvironment() []string {
	return append([]string(nil), dockerEnvironment()...)
}
