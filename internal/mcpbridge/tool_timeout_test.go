package mcpbridge

import (
	"testing"
	"time"
)

func TestToolCallTimeoutCoversTheLongestHumanInputWait(t *testing.T) {
	if MaxHumanInputWait != 7*time.Hour+30*time.Minute {
		t.Fatalf("MaxHumanInputWait = %v", MaxHumanInputWait)
	}
	if got := ToolCallTimeout(HumanInputTool); got != MaxHumanInputWait+2*time.Minute {
		t.Fatalf("human input call timeout = %v", got)
	}
	for _, tool := range []string{"platform_report_progress", "platform_publish_test_results", "platform_get_job_context"} {
		if got := ToolCallTimeout(tool); got != 31*time.Minute {
			t.Fatalf("%s call timeout = %v", tool, got)
		}
	}
}
