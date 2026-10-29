package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type recordingModelEndpoint struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	seen   chan struct{}
	once   sync.Once
	marker string
}

func newRecordingModelEndpoint(t *testing.T, marker string) *recordingModelEndpoint {
	t.Helper()
	endpoint := &recordingModelEndpoint{seen: make(chan struct{}), marker: marker}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		endpoint.mu.Lock()
		endpoint.bodies = append(endpoint.bodies, body)
		endpoint.mu.Unlock()
		if bytes.Contains(body, []byte(marker)) {
			endpoint.once.Do(func() { close(endpoint.seen) })
		}
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"offline smoke endpoint","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (endpoint *recordingModelEndpoint) bodyWith(marker string) []byte {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	for _, body := range endpoint.bodies {
		if bytes.Contains(body, []byte(marker)) {
			return body
		}
	}
	return nil
}

func requireProviderStdinSmoke(t *testing.T, binary string) string {
	t.Helper()
	if os.Getenv("RUN_PROVIDER_STDIN_SMOKE") != "1" {
		t.Skip("set RUN_PROVIDER_STDIN_SMOKE=1 to run the provider stdin smoke tests")
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Skipf("%s is not on PATH", binary)
	}
	return path
}

func smokePrompt(start, end string) string {
	var b strings.Builder
	b.WriteString(start + "\n")
	for line := 0; b.Len() < 200<<10; line++ {
		fmt.Fprintf(&b, "ticket description line %06d\n", line)
	}
	b.WriteString(end)
	return b.String()
}

func isolatedCLIEnvironment(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		dir := filepath.Join(root, strings.ToLower(name))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, name+"="+dir)
	}
	return env
}

func runUntilPromptReachesEndpoint(t *testing.T, endpoint *recordingModelEndpoint, binary string, args, env []string, dir string, stdin []byte) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	command.Dir = dir
	command.Stdin = bytes.NewReader(stdin)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case <-endpoint.seen:
		cancel()
		<-waited
	case err := <-waited:
		if endpoint.bodyWith(endpoint.marker) == nil {
			t.Fatalf("%s exited (%v) before sending the stdin prompt: %s", filepath.Base(binary), err, output.String())
		}
	case <-ctx.Done():
		t.Fatalf("%s never sent the stdin prompt: %s", filepath.Base(binary), output.String())
	}
	return output.String()
}

func assertEndpointReceivedPrompt(t *testing.T, endpoint *recordingModelEndpoint, prompt, start, end string) {
	t.Helper()
	body := endpoint.bodyWith(start)
	if body == nil || !bytes.Contains(body, []byte(end)) {
		t.Fatal("model request lacks the stdin prompt sentinels")
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, encoded[1:len(encoded)-1]) {
		t.Fatal("model request does not carry the complete stdin prompt")
	}
}

type codexSmokeRun struct {
	binary    string
	codexHome string
	workDir   string
	env       []string
}

func (run codexSmokeRun) send(t *testing.T, resumeSessionID, start, end string) string {
	t.Helper()
	prompt := smokePrompt(start, end)
	endpoint := newRecordingModelEndpoint(t, start)
	p := StartParams{
		Job:     v1.JobSpec{JobID: "fr08-smoke", AttemptID: "fr08-smoke-1", Kind: v1.JobKindBuild, FencingToken: 1},
		WorkDir: run.workDir, ConfigDir: t.TempDir(), MCPSock: "/nonexistent/mcp.sock",
		MCPTokenFile: "/nonexistent/job.token", MCPBinary: "/bin/false", Prompt: prompt,
		ResumeSessionID: resumeSessionID,
	}
	spec, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Args[len(spec.Args)-1] != "-" {
		t.Fatalf("codex args do not end with the stdin marker: %q", spec.Args)
	}
	stdin, err := io.ReadAll(spec.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string(nil), spec.Args[:2]...)
	args = append(args,
		"--skip-git-repo-check",
		"-c", `model_provider="fr08"`, "-c", `model="fr08-model"`,
		"-c", `model_providers.fr08.name="fr08"`,
		"-c", fmt.Sprintf(`model_providers.fr08.base_url=%q`, endpoint.server.URL+"/v1"),
		"-c", `model_providers.fr08.wire_api="responses"`,
		"-c", `model_providers.fr08.request_max_retries=0`,
		"-c", `model_providers.fr08.stream_max_retries=0`,
	)
	args = append(args, spec.Args[2:]...)
	output := runUntilPromptReachesEndpoint(t, endpoint, run.binary, args, append(append([]string(nil), run.env...), "CODEX_HOME="+run.codexHome), run.workDir, stdin)
	assertEndpointReceivedPrompt(t, endpoint, prompt, start, end)
	return output
}

func codexThreadID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" && event.ThreadID != "" {
			return event.ThreadID
		}
	}
	t.Fatalf("codex output has no thread.started event: %s", output)
	return ""
}

func TestCodexStdinSmokeReadsTheJobPromptFromStdin(t *testing.T) {
	run := codexSmokeRun{
		binary:    requireProviderStdinSmoke(t, "codex"),
		codexHome: t.TempDir(),
		workDir:   t.TempDir(),
		env:       isolatedCLIEnvironment(t),
	}
	first := run.send(t, "", "FR08-CODEX-NEW-START", "FR08-CODEX-NEW-END")
	threadID := codexThreadID(t, first)
	run.send(t, threadID, "FR08-CODEX-RESUME-START", "FR08-CODEX-RESUME-END")
}
