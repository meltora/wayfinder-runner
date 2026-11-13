package provider

import (
	"regexp"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxPendingClaudeMessages = 4096

var claudeMessageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type usageCounters struct {
	input      uint64
	output     uint64
	cacheRead  *uint64
	cacheWrite *uint64
}

func (a *UsageAccumulator) BudgetedTokens() (uint64, bool) {
	if a == nil {
		return 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	totals, ok := a.totals()
	if !ok {
		return 0, false
	}
	input := totals.input
	if a.provider == "codex" {
		input -= min(input, valueOrZero(totals.cacheRead))
	}
	total, ok := checkedSum(input, totals.output, valueOrZero(totals.cacheWrite))
	return total, ok
}

func (a *UsageAccumulator) totals() (usageCounters, bool) {
	if a.invalid || (!a.reported && len(a.pending) == 0) {
		return usageCounters{}, false
	}
	totals := usageCounters{input: a.input, output: a.output, cacheRead: copyCount(a.cacheRead), cacheWrite: copyCount(a.cacheWrite)}
	for _, pending := range a.pending {
		input, inputOK := checkedSum(totals.input, pending.input)
		output, outputOK := checkedSum(totals.output, pending.output)
		cacheRead, readOK := addOptionalCount(totals.cacheRead, pending.cacheRead)
		cacheWrite, writeOK := addOptionalCount(totals.cacheWrite, pending.cacheWrite)
		if !inputOK || !outputOK || !readOK || !writeOK {
			return usageCounters{}, false
		}
		totals = usageCounters{input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite}
	}
	return totals, true
}

func (a *UsageAccumulator) observePendingClaudeMessage(message map[string]any) {
	id, _ := message["id"].(string)
	usage, ok := message["usage"].(map[string]any)
	if !ok || !claudeMessageIDPattern.MatchString(id) {
		return
	}
	input, inputOK := count(usage["input_tokens"])
	output, outputOK := count(usage["output_tokens"])
	cacheRead, readPresent, readOK := optionalCount(usage["cache_read_input_tokens"])
	cacheWrite, writePresent, writeOK := optionalCount(usage["cache_creation_input_tokens"])
	if !inputOK || !outputOK || !readOK || !writeOK {
		return
	}
	if _, known := a.pending[id]; !known && len(a.pending) >= maxPendingClaudeMessages {
		return
	}
	counters := usageCounters{input: input, output: output}
	if readPresent {
		counters.cacheRead = &cacheRead
	}
	if writePresent {
		counters.cacheWrite = &cacheWrite
	}
	if a.pending == nil {
		a.pending = map[string]usageCounters{}
	}
	a.pending[id] = counters
}

func checkedSum(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		if value > v1.ProviderUsageMaxCount-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func addOptionalCount(total, value *uint64) (*uint64, bool) {
	if value == nil {
		return total, true
	}
	sum, ok := checkedSum(valueOrZero(total), *value)
	if !ok {
		return nil, false
	}
	return &sum, true
}

func copyCount(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
