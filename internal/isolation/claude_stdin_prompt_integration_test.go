package isolation_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example/app-runner/internal/isolation"
)

const stdinPromptIntegrationRunner = "fr08-stdin-prompt"

const recordingMessagesAPI = `const http = require('http');
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
function record(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  const firstUser = messages.find((message) => message && message.role === 'user');
  const blocks = textBlocks(firstUser).map((text) => ({
    bytes: Buffer.byteLength(text, 'utf8'),
    sha256: crypto.createHash('sha256').update(text, 'utf8').digest('hex'),
    has_start: text.includes(start), has_end: text.includes(end),
  }));
  fs.appendFileSync('/tmp/requests.jsonl', JSON.stringify({ index: index++, blocks }) + '\n');
}
function respond(res, body) {
  const message = {
    id: 'msg_offline', type: 'message', role: 'assistant', model: body.model || 'offline',
    content: [], stop_reason: null, stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1, cache_creation_input_tokens: 0, cache_read_input_tokens: 0 },
  };
  res.writeHead(200, { 'content-type': 'text/event-stream', 'request-id': 'req_offline' });
  const send = (event, data) => res.write('event: ' + event + '\ndata: ' + JSON.stringify(data) + '\n\n');
  send('message_start', { type: 'message_start', message });
  send('content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } });
  send('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'done' } });
  send('content_block_stop', { type: 'content_block_stop', index: 0 });
  send('message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 1 } });
  send('message_stop', { type: 'message_stop' });
  res.end();
}
http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (chunk) => { chunks.push(chunk); });
  req.on('end', () => {
    let body = {};
    try { body = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}'); } catch (error) { body = {}; }
    if (!req.url.startsWith('/v1/messages') || req.url.includes('count_tokens')) {
      res.writeHead(404, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'offline' } }));
      return;
    }
    record(body);
    respond(res, body);
  });
}).listen(8099, '127.0.0.1');
`

const offlineClaudeStdinScript = `set -u
mkdir -p /home/runner/work
node /probe/messages-api.js &
sleep 1
export ANTHROPIC_BASE_URL=http://127.0.0.1:8099 ANTHROPIC_API_KEY=offline-probe-key CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
cd /home/runner/work
timeout 120 claude "$@" > /tmp/claude.out 2>&1
echo "==claude-exit:$?"
echo "==requests"
cat /tmp/requests.jsonl 2>/dev/null
echo "==claude-out"
tail -c 4000 /tmp/claude.out
echo "==end"
`

type recordedTextBlock struct {
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
	HasStart bool   `json:"has_start"`
	HasEnd   bool   `json:"has_end"`
}

type recordedRequest struct {
	Index  int                 `json:"index"`
	Blocks []recordedTextBlock `json:"blocks"`
}

func largeStdinPrompt(start, end string) string {
	var b strings.Builder
	b.WriteString("You are implementing the approved plan for ticket PROJ-1: stdin delivery\n\n")
	b.WriteString("Ticket background (the planner's input, not the Build scope):\n")
	b.WriteString(start + "\n")
	for line := 0; b.Len() < 256<<10; line++ {
		fmt.Fprintf(&b, "Description line %06d keeps the ticket background above the single-argument limit.\n", line)
	}
	b.WriteString(end + "\n")
	b.WriteString("\nFirst call the platform_get_job_context MCP tool for full context. Reply with done.")
	return b.String()
}

func stdinPromptDockerArgs(ctx context.Context, t *testing.T, image, name, probeName, probeSource string, env []string) []string {
	t.Helper()
	mapping, err := isolation.ResolveUserMapping(ctx, image, stdinPromptIntegrationRunner)
	if err != nil {
		t.Fatal(err)
	}
	probeDir := t.TempDir()
	writeProbeFile(t, filepath.Join(probeDir, probeName), probeSource, 0o644)
	if err := os.Chmod(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	user := mapping.User()
	uid, gid, _ := strings.Cut(user, ":")
	dockerArgs := []string{"run", "--rm", "-i", "--init", "--user", user, "--name", name,
		"--cap-drop", "ALL", "--cap-add", "SYS_ADMIN", "--cap-add", "SYS_CHROOT",
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--security-opt", "seccomp=unconfined",
		"--label", "com.ourplatform.runner-id=" + stdinPromptIntegrationRunner,
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/runner:rw,nosuid,nodev,size=64m,uid=" + uid + ",gid=" + gid,
		"-e", "HOME=/home/runner",
		"-e", "PATH=" + isolation.UnprivilegedBubblewrapDir + ":" + isolation.JobImagePath,
		"-v", probeDir + ":/probe:ro",
	}
	for _, entry := range env {
		dockerArgs = append(dockerArgs, "-e", entry)
	}
	return dockerArgs
}

func uniqueContainerName(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return stdinPromptIntegrationRunner + "-" + hex.EncodeToString(suffix)
}

func assertNoStdinPromptContainersRemain(t *testing.T) {
	t.Helper()
	output, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+stdinPromptIntegrationRunner).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("stdin prompt probe containers remain: %s", output)
	}
}

func parseStdinPromptRun(t *testing.T, output string) (string, []recordedRequest) {
	t.Helper()
	exit := ""
	section := ""
	var requests []recordedRequest
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "==claude-exit:"):
			exit = strings.TrimPrefix(line, "==claude-exit:")
		case strings.HasPrefix(line, "==opencode-exit:"):
			exit = strings.TrimPrefix(line, "==opencode-exit:")
		case line == "==requests" || line == "==claude-out" || line == "==opencode-out" || line == "==end":
			section = line
		case section == "==requests" && strings.TrimSpace(line) != "":
			var request recordedRequest
			if err := json.Unmarshal([]byte(line), &request); err != nil {
				t.Fatalf("recorded request %q: %v", line, err)
			}
			requests = append(requests, request)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return exit, requests
}

func TestClaudeReadsALargePromptFromStdinInTheJobImage(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	start, end := "FR08-STDIN-START-7c41", "FR08-STDIN-END-2e9d"
	args, specFlags, stdin := offlineClaudeBuildSpec(t, largeStdinPrompt(start, end))
	if len(stdin) <= 128<<10 {
		t.Fatalf("prompt is only %d bytes", len(stdin))
	}
	for _, arg := range args {
		if strings.Contains(arg, start) || strings.Contains(arg, end) {
			t.Fatalf("Claude argv carries the prompt: %.80q", arg)
		}
	}
	promptPath := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(promptPath, stdin, 0o600); err != nil {
		t.Fatal(err)
	}
	dockerArgs := stdinPromptDockerArgs(ctx, t, image, uniqueContainerName(t), "messages-api.js", recordingMessagesAPI, []string{
		"PROMPT_START_SENTINEL=" + start, "PROMPT_END_SENTINEL=" + end,
	})
	dockerArgs = append(dockerArgs, specFlags...)
	dockerArgs = append(dockerArgs, image, "sh", "-c", offlineClaudeStdinScript, "offline-claude-stdin")
	dockerArgs = append(dockerArgs, args...)

	control := exec.CommandContext(ctx, "docker", append(append([]string(nil), dockerArgs...), string(stdin))...)
	if err := control.Start(); err == nil {
		_ = control.Process.Kill()
		_ = control.Wait()
		t.Fatal("argv delivery of the prompt started docker")
	} else if !errors.Is(err, syscall.E2BIG) {
		t.Fatalf("argv delivery failed with %v, want E2BIG", err)
	}

	promptFile, err := os.Open(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = promptFile.Close() }()
	command := exec.CommandContext(ctx, "docker", dockerArgs...)
	command.Stdin = promptFile
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline claude stdin probe failed: %v: %s", err, output)
	}
	assertNoStdinPromptContainersRemain(t)
	exit, requests := parseStdinPromptRun(t, string(output))
	if exit != "0" {
		t.Fatalf("claude exit %q: %s", exit, output)
	}
	if len(requests) == 0 {
		t.Fatalf("fake Messages API recorded no request: %s", output)
	}
	sum := sha256.Sum256(stdin)
	want := hex.EncodeToString(sum[:])
	first := requests[0]
	matched := false
	for _, block := range first.Blocks {
		if block.HasStart && block.HasEnd && block.Bytes == len(stdin) && block.SHA256 == want {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("first request user text %+v does not equal the %d-byte prompt %s", first.Blocks, len(stdin), want)
	}
	t.Logf("first request carried the %d-byte prompt in one of %d user text blocks (%d requests)", len(stdin), len(first.Blocks), len(requests))
}
