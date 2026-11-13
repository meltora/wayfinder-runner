package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJobFinishedCarriesProviderUsageSummedAcrossProviderProcesses(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Provider = "claude"
	jr.consumeOutput(bytes.NewBufferString(strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-5-5"}`,
		`{"type":"result","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":7}}`,
	}, "\n") + "\n"))
	jr.consumeOutput(bytes.NewBufferString(`{"type":"result","usage":{"input_tokens":1,"output_tokens":2}}` + "\n"))

	event := jr.jobFinishedEvent(jobOutcome{State: "succeeded"})
	raw, err := json.Marshal(event.ProviderUsage)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"provider":"claude","model":"claude-opus-5-5","input_tokens":101,"output_tokens":22,"cache_read_input_tokens":7}`
	if string(raw) != want {
		t.Fatalf("provider usage = %s, want %s", raw, want)
	}
}

func TestJobFinishedOmitsProviderUsageWithoutStructuredCounters(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Provider = "codex"
	jr.consumeOutput(bytes.NewBufferString(`{"type":"thread.started","thread_id":"t1"}` + "\n" + "plain text usage: 9000 tokens\n"))

	if usage := jr.jobFinishedEvent(jobOutcome{State: "succeeded"}).ProviderUsage; usage != nil {
		t.Fatalf("provider usage = %+v", usage)
	}
}
