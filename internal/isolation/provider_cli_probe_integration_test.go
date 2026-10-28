package isolation_test

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
)

const providerCLIIntegrationRunner = "rjic-provider-cli"

func requireDockerSandboxGate(t *testing.T, variable string) string {
	t.Helper()
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv(variable)
	if image == "" {
		if variable == "JOB_IMAGE" {
			t.Fatal("JOB_IMAGE is required")
		}
		t.Skip("set " + variable + " to run this case")
	}
	return image
}

func probeClaudeIntegration(t *testing.T, image string) (isolation.ProviderCLIProbe, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, providerCLIIntegrationRunner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := isolation.ProbeProviderCLI(ctx, image, "claude", providerCLIIntegrationRunner, mapping)
	if err != nil {
		t.Fatal(err)
	}
	assertNoProbeContainersRemain(t)
	return result, provider.MissingCLIOptions(result.Help, provider.NewClaude().RequiredCLIOptions())
}

func assertNoProbeContainersRemain(t *testing.T) {
	t.Helper()
	output, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+providerCLIIntegrationRunner).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("probe containers remain: %s", output)
	}
}

func TestProviderCLIProbeIntegrationAcceptsEveryRunnerOption(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	result, missing := probeClaudeIntegration(t, image)
	t.Logf("job image claude version: %s", result.Version)
	if len(missing) != 0 {
		t.Fatalf("%s is missing runner options %q", result.Version, missing)
	}
}

func TestProviderCLIProbeIntegrationReportsTheOldImagesMissingOptions(t *testing.T) {
	image := requireDockerSandboxGate(t, "INCOMPATIBLE_JOB_IMAGE")
	result, missing := probeClaudeIntegration(t, image)
	t.Logf("incompatible image claude version: %s", result.Version)
	if want := []string{"--permission-prompts", "--restricted"}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %q, want %q", missing, want)
	}
}

const weakerSandboxProbeScript = `set -u
mkdir -p /home/runner/secret
echo TOPSECRET > /home/runner/secret/cred.txt
printf '%s' '{"enableWeakerNestedSandbox":true,"network":{"allowedDomains":[],"deniedDomains":[]},"filesystem":{"denyRead":["/home/runner/secret"],"allowWrite":["/tmp"],"denyWrite":[]}}' > /tmp/weak.json
env PARENT_SECRET=ENVSECRET sleep 60 &
holder=$!
echo "bwrap:$(command -v bwrap)"
run() { srt -s /tmp/weak.json -c "echo started; $1" 2>&1; }
check() { output=$(run "$2"); echo "$1:$(printf '%s\n' "$output" | grep -c '^started$'):$(printf '%s\n' "$output" | grep -c "$3")"; }
echo "echo:$(run 'echo sandbox-ok' | tail -1)"
check read 'cat /home/runner/secret/cred.txt' TOPSECRET
check procroot 'for p in /proc/[0-9]*; do cat $p/root/home/runner/secret/cred.txt 2>/dev/null; done; true' TOPSECRET
check write 'touch /home/runner/outside' started-never-matches
if [ -e /home/runner/outside ]; then echo write:allowed; else echo write:refused; fi
check environ "cat /proc/$holder/environ 2>/dev/null | tr '\0' '\n'" ENVSECRET
echo "environ-outside:$(tr '\0' '\n' < /proc/$holder/environ | grep -c ENVSECRET)"
kill "$holder"
`

func TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, providerCLIIntegrationRunner)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, _ := strings.Cut(mapping.User(), ":")
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--init", "--user", mapping.User(),
		"--cap-drop", "ALL", "--cap-add", "SYS_ADMIN", "--cap-add", "SYS_CHROOT",
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--security-opt", "seccomp=unconfined",
		"--label", "com.ourplatform.runner-id="+providerCLIIntegrationRunner,
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/runner:rw,nosuid,nodev,size=16m,uid="+uid+",gid="+gid,
		"-e", "PATH="+isolation.UnprivilegedBubblewrapDir+":"+isolation.JobImagePath,
		image, "sh", "-c", weakerSandboxProbeScript)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("weaker sandbox probe failed: %v: %s", err, output)
	}
	got := strings.Split(strings.TrimSpace(string(output)), "\n")
	want := []string{"bwrap:" + isolation.UnprivilegedBubblewrapDir + "/bwrap", "echo:sandbox-ok", "read:1:0", "procroot:1:0", "write:1:0", "write:refused", "environ:1:0", "environ-outside:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("weaker sandbox probe = %q, want %q", got, want)
	}
	rg, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", mapping.User(), "--network", "none", "--read-only",
		"--label", "com.ourplatform.runner-id="+providerCLIIntegrationRunner, image, "rg", "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(rg), "ripgrep ") {
		t.Fatalf("rg --version: %v: %s", err, rg)
	}
	setID, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", "0:0", "--network", "none", "--read-only",
		"--label", "com.ourplatform.runner-id="+providerCLIIntegrationRunner, image,
		"find", "/", "-xdev", "-perm", "/6000", "-type", "f").Output()
	if err != nil {
		t.Fatalf("find set-id files: %v", err)
	}
	if got := strings.TrimSpace(string(setID)); got != "/usr/bin/bwrap" {
		t.Fatalf("set-id files = %q", got)
	}
	inventory, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", mapping.User(), "--network", "none", "--read-only",
		"--label", "com.ourplatform.runner-id="+providerCLIIntegrationRunner, image, "sh", "-c",
		`printf '%s\n' "$PATH"; stat -c '%a %U:%G' "$0/bwrap"; cmp /usr/bin/bwrap "$0/bwrap" && echo identical`,
		isolation.UnprivilegedBubblewrapDir).CombinedOutput()
	if err != nil {
		t.Fatalf("unprivileged bwrap inventory: %v: %s", err, inventory)
	}
	if want := isolation.JobImagePath + "\n755 root:root\nidentical\n"; string(inventory) != want {
		t.Fatalf("unprivileged bwrap inventory = %q, want %q", inventory, want)
	}
	assertNoProbeContainersRemain(t)
}
