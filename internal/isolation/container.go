package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
	if networkName == "" || policyID == "" {
		return fmt.Errorf("network name and policy ID are required")
	}
	// #nosec G204 -- executable/flags are fixed and networkName is validated configuration, passed as one argv item.
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", "--format", "{{json .Labels}}", networkName)
	cmd.Env = dockerEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect restricted Docker network: %w", err)
	}
	var labels map[string]string
	if err := json.Unmarshal(out, &labels); err != nil {
		return fmt.Errorf("decode Docker network labels: %w", err)
	}
	if labels["com.ourplatform.egress-enforced"] != "true" || labels["com.ourplatform.egress-policy"] != policyID {
		return fmt.Errorf("Docker network is not bound to enforced egress policy %q", policyID)
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
