package isolation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/provider"
)

const recordingChatCompletionsAPI = `const http = require('http');
const crypto = require('crypto');
const fs = require('fs');
const start = process.env.PROMPT_START_SENTINEL;
const end = process.env.PROMPT_END_SENTINEL;
let index = 0;
function textBlocks(message) {
  if (!message) return [];
  if (typeof message.content === 'string') return [message.content];
  if (!Array.isArray(message.content)) return [];
  return message.content.filter((block) => block && block.type === 'text').map((block) => block.text);
}
http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (chunk) => { chunks.push(chunk); });
  req.on('end', () => {
    let body = {};
    try { body = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}'); } catch (error) { body = {}; }
    if (req.url.startsWith('/v1/chat/completions')) {
      const messages = Array.isArray(body.messages) ? body.messages : [];
      const blocks = messages.filter((message) => message && message.role === 'user').flatMap(textBlocks).map((text) => ({
        bytes: Buffer.byteLength(text, 'utf8'),
        sha256: crypto.createHash('sha256').update(text, 'utf8').digest('hex'),
        has_start: text.includes(start), has_end: text.includes(end),
      }));
      fs.appendFileSync('/tmp/requests.jsonl', JSON.stringify({ index: index++, blocks }) + '\n');
    }
    res.writeHead(400, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: { message: 'offline stdin probe', type: 'invalid_request_error' } }));
  });
}).listen(8098, '127.0.0.1');
`

const offlineOpenCodeStdinScript = `set -u
mkdir -p /home/runner/work
node /probe/chat-completions-api.js &
sleep 1
cd /home/runner/work
timeout 120 opencode "$@" > /tmp/opencode.out 2>&1
echo "==opencode-exit:$?"
echo "==requests"
cat /tmp/requests.jsonl 2>/dev/null
echo "==opencode-out"
tail -c 4000 /tmp/opencode.out
echo "==end"
`

func offlineOpenCodeBuildSpec(t *testing.T, prompt string) ([]string, []string, []byte) {
	t.Helper()
	spec, err := provider.NewOpenCode().BuildSpec(provider.StartParams{
		Job:             v1.JobSpec{JobID: "fr08-offline", AttemptID: "fr08-offline-1", Kind: v1.JobKindBuild, FencingToken: 1},
		WorkDir:         "/home/runner/work",
		ConfigDir:       t.TempDir(),
		MCPSock:         "/run/job/mcp.sock",
		MCPTokenFile:    "/run/job/job.token",
		MCPBinary:       "/usr/local/bin/our-runner",
		Prompt:          prompt,
		Model:           "fr08/fr08-model",
		HardenedRuntime: true,
		DockerIsolation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Stdin == nil {
		t.Fatal("OpenCode job spec has no stdin prompt")
	}
	stdin, err := io.ReadAll(spec.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	var flags []string
	for _, entry := range provider.ContainerEnvironment(spec.Env) {
		key, value, _ := strings.Cut(entry, "=")
		if key == "OPENCODE_CONFIG_CONTENT" {
			entry = "OPENCODE_CONFIG_CONTENT=" + offlineOpenCodeConfig(t, value)
		}
		flags = append(flags, "-e", entry)
	}
	return append([]string(nil), spec.Args...), flags, stdin
}

func offlineOpenCodeConfig(t *testing.T, value string) string {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		t.Fatal(err)
	}
	config["mcp"] = map[string]any{}
	config["provider"] = map[string]any{"fr08": map[string]any{
		"npm": "@ai-sdk/openai-compatible", "name": "fr08",
		"options": map[string]any{"baseURL": "http://127.0.0.1:8098/v1", "apiKey": "offline"},
		"models":  map[string]any{"fr08-model": map[string]any{"name": "fr08-model"}},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOpenCodeReadsALargePromptFromStdinInTheJobImage(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	start, end := "FR08-OC-STDIN-START-4b2a", "FR08-OC-STDIN-END-91fe"
	args, specFlags, stdin := offlineOpenCodeBuildSpec(t, largeStdinPrompt(start, end))
	if len(stdin) <= 128<<10 {
		t.Fatalf("prompt is only %d bytes", len(stdin))
	}
	for _, arg := range args {
		if strings.Contains(arg, start) || strings.Contains(arg, end) {
			t.Fatalf("OpenCode argv carries the prompt: %.80q", arg)
		}
	}
	promptPath := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(promptPath, stdin, 0o600); err != nil {
		t.Fatal(err)
	}
	dockerArgs := stdinPromptDockerArgs(ctx, t, image, uniqueContainerName(t), "chat-completions-api.js", recordingChatCompletionsAPI, []string{
		"PROMPT_START_SENTINEL=" + start, "PROMPT_END_SENTINEL=" + end,
	})
	dockerArgs = append(dockerArgs, specFlags...)
	dockerArgs = append(dockerArgs, image, "sh", "-c", offlineOpenCodeStdinScript, "offline-opencode-stdin")
	dockerArgs = append(dockerArgs, args...)

	promptFile, err := os.Open(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = promptFile.Close() }()
	command := exec.CommandContext(ctx, "docker", dockerArgs...)
	command.Stdin = promptFile
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline opencode stdin probe failed: %v: %s", err, output)
	}
	assertNoStdinPromptContainersRemain(t)
	exit, requests := parseStdinPromptRun(t, string(output))
	if exit == "" || exit == "124" {
		t.Fatalf("opencode exit %q: %s", exit, output)
	}
	if len(requests) == 0 {
		t.Fatalf("fake Chat Completions API recorded no request: %s", output)
	}
	sum := sha256.Sum256(stdin)
	want := hex.EncodeToString(sum[:])
	matched := false
	for _, block := range requests[0].Blocks {
		if block.HasStart && block.HasEnd && block.Bytes == len(stdin) && block.SHA256 == want {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("first request user text %+v does not equal the %d-byte prompt %s: %s", requests[0].Blocks, len(stdin), want, output)
	}
	t.Logf("first request carried the %d-byte prompt (%d requests, opencode exit %s against the refusing endpoint)", len(stdin), len(requests), exit)
}
