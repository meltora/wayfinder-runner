package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

func buildStaticRunner(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "our-runner")
	cmd := exec.Command("go", "build", "-o", out, "github.com/example/app-runner/cmd/our-runner")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the runner: %v: %s", err, output)
	}
	if err := os.Chmod(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIntegrationExchangeLoginReplacesTheHandedOffJobCopy(t *testing.T) {
	image, mapping := rootlessGate(t)
	exe := buildStaticRunner(t)
	state := integrationState(t)
	home := filepath.Join(state, "job-homes", "home-exchange")
	mkdirs(t, 0o700, filepath.Join(home, ".claude"))
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"fake-job-access","refreshToken":"fake-job-refresh","expiresAt":100}}`, 0o600)
	ownership := DockerOwnership{Mapping: mapping, Image: image, StateDir: state, Executable: func() (string, error) { return exe, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request := OwnershipRequest{RunnerID: "itest", AttemptID: "exchange", Purpose: "handoff", Private: []string{home}}
	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed := false
	defer func() {
		if !reclaimed {
			request.Purpose = "reclaim"
			_ = ownership.Reclaim(context.Background(), request)
		}
	}()
	result, err := ownership.ExchangeLogin(ctx, "itest", "exchange", home, claudelogin.ExchangeRequest{
		Host:     json.RawMessage(`{"accessToken":"fake-host-access","refreshToken":"fake-host-refresh","expiresAt":200}`),
		Baseline: sha256.Sum256([]byte("fake-job-refresh")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != claudelogin.ReasonOK || !result.Replaced {
		t.Fatalf("result = %s replaced=%v", result.Reason, result.Replaced)
	}
	ownedByJob := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", "10001:10001", "--network", "none", "--cap-drop", "ALL",
		"-v", home+":/home/runner:ro", image, "sh", "-c", `f=/home/runner/.claude/.credentials.json; test -O "$f" && test "$(stat -c %a "$f")" = 600`)
	ownedByJob.Env = dockerEnvironment()
	if output, err := ownedByJob.CombinedOutput(); err != nil {
		t.Fatalf("the job copy is not a 0600 file of the job user: %v: %s", err, output)
	}
	credentials := filepath.Join(home, ".claude", ".credentials.json")
	request.Purpose = "reclaim"
	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed = true
	data, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	login, err := claudelogin.ParseCredentials(data)
	if err != nil || login.RefreshHash != sha256.Sum256([]byte("fake-host-refresh")) {
		t.Fatalf("job copy was not replaced: %v", err)
	}
	for _, lock := range []string{".claude/.oauth_refresh.lock", ".claude.lock"} {
		if _, err := os.Lstat(filepath.Join(home, lock)); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind", lock)
		}
	}
}
