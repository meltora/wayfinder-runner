package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/example/app-runner/internal/supervisor"
)

func TestProviderOutputTailSanitizesRedactsAndBoundsEachLine(t *testing.T) {
	tail := newProviderOutputTail([]string{
		"ANTHROPIC_BASE_URL=https://proxy.example.test/private", "SHORT=abc1234", "EMPTY=", "MALFORMED",
	}, "credential-token-value")
	for _, line := range []string{
		"error: unknown option '--restricted'",
		"Authorization: Bearer abcdefghijklmnop",
		strings.Repeat("x", 10<<10),
		"connect https://proxy.example.test/private failed",
		"short abc1234 stays",
		"bad\xffutf8\r\x00\x1b[31mred\u009b",
		strings.Repeat("é", 300),
		"push with credential-token-value refused",
	} {
		tail.add([]byte(line))
	}
	want := []string{
		"error: unknown option '--restricted'",
		"[redacted]",
		strings.Repeat("x", 256),
		"connect [redacted] failed",
		"short abc1234 stays",
		"bad�utf8   [31mred ",
		strings.Repeat("é", 128),
		"push with [redacted] refused",
	}
	if got := tail.snapshot(); !equalStrings(got, want) {
		t.Fatalf("lines = %q\nwant %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func TestProviderOutputTailKeepsTheNewestSixteenLines(t *testing.T) {
	tail := newProviderOutputTail(nil, "")
	if tail.String() != "" {
		t.Fatalf("empty tail = %q", tail.String())
	}
	for index := 0; index < 40; index++ {
		tail.add([]byte(fmt.Sprintf("line-%02d", index)))
	}
	lines := tail.snapshot()
	if len(lines) != 16 || lines[0] != "line-24" || lines[15] != "line-39" {
		t.Fatalf("lines = %q", lines)
	}
	if got := tail.String(); !strings.HasPrefix(got, "line-24 | line-25") || !strings.HasSuffix(got, "line-38 | line-39") {
		t.Fatalf("tail = %q", got)
	}
}

func TestProviderOutputTailDropsTheOldestLinesToFit2048Bytes(t *testing.T) {
	tail := newProviderOutputTail(nil, "")
	for index := 0; index < 16; index++ {
		tail.add([]byte(fmt.Sprintf("%02d", index) + strings.Repeat("y", 300)))
	}
	got := tail.String()
	if len(got) > 2048 {
		t.Fatalf("tail length = %d", len(got))
	}
	parts := strings.Split(got, " | ")
	if len(parts) != 7 || !strings.HasPrefix(parts[0], "09") || !strings.HasPrefix(parts[6], "15") {
		t.Fatalf("kept %d lines starting %q", len(parts), got[:8])
	}
}

func TestProviderOutputTailRecognisesOnlyAWellFormedRejectedOption(t *testing.T) {
	for name, test := range map[string]struct {
		lines []string
		want  string
	}{
		"long option":         {[]string{"error: unknown option '--restricted'"}, "--restricted"},
		"short option":        {[]string{"error: unknown option '-x'"}, "-x"},
		"newest match":        {[]string{"error: unknown option '--first'", "noise", "error: unknown option '--second'"}, "--second"},
		"whitespace":          {[]string{"error: unknown option '--bad option'"}, ""},
		"no dash":             {[]string{"error: unknown option 'restricted'"}, ""},
		"too long":            {[]string{"error: unknown option '--" + strings.Repeat("a", 65) + "'"}, ""},
		"quote injection":     {[]string{"error: unknown option '--x'; rm'"}, "--x"},
		"other error":         {[]string{"error: missing required argument"}, ""},
		"none":                {nil, ""},
		"leading underscore":  {[]string{"error: unknown option '--_x'"}, ""},
		"maximum length":      {[]string{"error: unknown option '--" + strings.Repeat("a", 64) + "'"}, "--" + strings.Repeat("a", 64)},
		"triple dash refused": {[]string{"error: unknown option '---x'"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			tail := newProviderOutputTail(nil, "")
			for _, line := range test.lines {
				tail.add([]byte(line))
			}
			if got := tail.rejectedOption(); got != test.want {
				t.Fatalf("rejected option = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConsumeOutputRetainsOnlyUnstructuredLines(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Provider = "claude"
	jr.d.cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"ANTHROPIC_BASE_URL": "https://proxy.example.test/private"}}
	jr.consumeOutput(bytes.NewBufferString(strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		"warning via https://proxy.example.test/private",
		`{"type":"result","is_error":true,"result":"Bearer abcdefghijklmnopqrst"}`,
		"error: unknown option '--restricted'",
	}, "\n") + "\n"))
	got := jr.currentOutputTail().String()
	if got != "warning via [redacted] | error: unknown option '--restricted'" {
		t.Fatalf("tail = %q", got)
	}
	if jr.currentProviderError() != "[redacted]" {
		t.Fatalf("provider error = %q", jr.currentProviderError())
	}
}

type outputTestProcess struct {
	done    chan struct{}
	waitErr error
}

func (outputTestProcess) PID() int            { return 4545 }
func (process outputTestProcess) Wait() error { <-process.done; return process.waitErr }
func (outputTestProcess) Interrupt() error    { return nil }
func (outputTestProcess) Kill() error         { return nil }
func (*outputTestSupervisor) Name() string    { return "output-test" }
func (*outputTestSupervisor) Available() bool { return true }

type outputTestSupervisor struct {
	output    string
	waitErr   error
	duringRun func()
}

func (value *outputTestSupervisor) Start(_ context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(spec.Stdout, value.output)
		if value.duringRun != nil {
			value.duringRun()
		}
	}()
	return outputTestProcess{done: done, waitErr: value.waitErr}, nil
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) records(t *testing.T, message string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == message {
			out = append(out, record)
		}
	}
	return out
}

func runProviderOutputJob(t *testing.T, sup *outputTestSupervisor) (*ownershipRun, *syncBuffer) {
	t.Helper()
	run := newOwnershipRun(t, testIdentityMapping)
	logs := &syncBuffer{}
	run.jr.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	run.jr.d.supervisor = sup
	run.compositionRun.execute(t)
	return run, logs
}

func TestAgentExitLogsTheBoundedTailAndClassifiesARejectedOption(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"result","is_error":true,"result":"startup failed"}`,
		"Authorization: Bearer abcdefghijklmnop",
		"error: unknown option '--restricted'",
	}, "\n") + "\n"
	run, logs := runProviderOutputJob(t, &outputTestSupervisor{output: output, waitErr: errors.New("exit status 1")})
	run.expectFinished(t, "failed", "agent_exit_failed")
	finished := run.finished(t)
	message := str(finished, "failure_message")
	if message != "agent process exited unsuccessfully: the provider CLI rejected option --restricted" {
		t.Fatalf("failure_message = %q", message)
	}
	if len(message) > 512 {
		t.Fatalf("failure_message is %d bytes", len(message))
	}
	records := logs.records(t, "agent exited")
	if len(records) != 1 {
		t.Fatalf("agent exited records = %v", records)
	}
	if records[0]["provider_output_tail"] != "[redacted] | error: unknown option '--restricted'" || records[0]["provider_error"] != "startup failed" {
		t.Fatalf("agent exited record = %v", records[0])
	}
	if len(logs.records(t, "unstructured agent output suppressed")) != 2 {
		t.Fatal("per-line suppression debug log changed")
	}
}

func TestAgentExitWithoutARecognisedCauseKeepsTheFixedMessage(t *testing.T) {
	for name, output := range map[string]string{
		"unmatched":        "Segmentation fault\n",
		"malformed option": "error: unknown option '--bad option'\n",
		"no output":        "",
	} {
		t.Run(name, func(t *testing.T) {
			run, logs := runProviderOutputJob(t, &outputTestSupervisor{output: output, waitErr: errors.New("exit status 1")})
			run.expectFinished(t, "failed", "agent_exit_failed")
			if message := str(run.finished(t), "failure_message"); message != "agent process exited unsuccessfully" {
				t.Fatalf("failure_message = %q", message)
			}
			records := logs.records(t, "agent exited")
			if len(records) != 1 {
				t.Fatalf("records = %v", records)
			}
			_, hasTail := records[0]["provider_output_tail"]
			_, hasError := records[0]["provider_error"]
			if hasTail != (output != "") || hasError {
				t.Fatalf("record = %v", records[0])
			}
		})
	}
}

func TestCancelledAgentNeverReportsTheRejectedOption(t *testing.T) {
	var run *ownershipRun
	sup := &outputTestSupervisor{output: "error: unknown option '--restricted'\n", waitErr: errors.New("killed")}
	sup.duringRun = func() {
		run.jr.procMu.Lock()
		run.jr.cancelled = true
		run.jr.procMu.Unlock()
	}
	run = newOwnershipRun(t, testIdentityMapping)
	run.jr.d.supervisor = sup
	run.compositionRun.execute(t)
	run.expectFinished(t, "cancelled", "cancelled")
	if message := str(run.finished(t), "failure_message"); strings.Contains(message, "rejected option") {
		t.Fatalf("failure_message = %q", message)
	}
}

func TestCapacityExhaustionNeverReportsTheRejectedOption(t *testing.T) {
	output := `{"type":"result","is_error":true,"result":"You've hit your limit. Your usage resets later.","retry_after_seconds":7200}` + "\nerror: unknown option '--restricted'\n"
	run, _ := runProviderOutputJob(t, &outputTestSupervisor{output: output, waitErr: errors.New("exit status 1")})
	for _, finished := range run.events.byKind("job_finished") {
		if strings.Contains(str(finished, "failure_message"), "rejected option") {
			t.Fatalf("capacity exhaustion reported a rejected option: %v", finished)
		}
	}
	if events := run.events.byKind("provider_capacity_exhausted"); len(events) != 1 {
		t.Fatalf("capacity events = %v", events)
	}
}

func TestSuccessfulAgentLogsNoTail(t *testing.T) {
	run, logs := runProviderOutputJob(t, &outputTestSupervisor{output: "error: unknown option '--restricted'\n"})
	if records := logs.records(t, "agent exited"); len(records) != 0 {
		t.Fatalf("successful run logged %v", records)
	}
	for _, finished := range run.events.byKind("job_finished") {
		if strings.Contains(str(finished, "failure_message"), "rejected option") {
			t.Fatalf("successful run reported %v", finished)
		}
	}
}
