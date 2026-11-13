package provider

import (
	"math"
	"regexp"
	"sync"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var usageModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

type UsageAccumulator struct {
	mu         sync.Mutex
	provider   string
	model      string
	reported   bool
	overflow   bool
	input      uint64
	output     uint64
	cacheRead  *uint64
	cacheWrite *uint64
}

func NewUsageAccumulator(adapter string) *UsageAccumulator {
	switch adapter {
	case "claude", "codex", "opencode":
		return &UsageAccumulator{provider: adapter}
	default:
		return nil
	}
}

func (a *UsageAccumulator) Observe(event map[string]any) {
	if a == nil || event == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.provider {
	case "claude":
		a.observeClaude(event)
	case "codex":
		a.observeCodex(event)
	case "opencode":
		a.observeOpenCode(event)
	}
}

func (a *UsageAccumulator) Snapshot() *v1.ProviderUsage {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.reported || a.overflow {
		return nil
	}
	usage := &v1.ProviderUsage{
		Provider:     a.provider,
		Model:        a.model,
		InputTokens:  a.input,
		OutputTokens: a.output,
	}
	if a.cacheRead != nil {
		value := *a.cacheRead
		usage.CacheReadInputTokens = &value
	}
	if a.cacheWrite != nil {
		value := *a.cacheWrite
		usage.CacheCreationInputTokens = &value
	}
	return usage
}

func (a *UsageAccumulator) observeClaude(event map[string]any) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "system":
		if subtype, _ := event["subtype"].(string); subtype == "init" {
			a.observeModel(event["model"])
		}
	case "assistant":
		if message, ok := event["message"].(map[string]any); ok && a.model == "" {
			a.observeModel(message["model"])
		}
	case "result":
		usage, ok := event["usage"].(map[string]any)
		if !ok {
			return
		}
		a.addCounters(
			usage["input_tokens"],
			usage["output_tokens"],
			usage["cache_read_input_tokens"],
			usage["cache_creation_input_tokens"],
		)
	}
}

func (a *UsageAccumulator) observeCodex(event map[string]any) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "thread.started", "session.created":
		a.observeModel(event["model"])
	case "turn.completed":
		a.observeModel(event["model"])
		usage, ok := event["usage"].(map[string]any)
		if !ok {
			return
		}
		a.addCounters(usage["input_tokens"], usage["output_tokens"], usage["cached_input_tokens"], nil)
	}
}

func (a *UsageAccumulator) observeOpenCode(event map[string]any) {
	eventType, _ := event["type"].(string)
	if eventType != "step_finish" {
		return
	}
	part, ok := event["part"].(map[string]any)
	if !ok {
		return
	}
	a.observeModel(part["modelID"])
	tokens, ok := part["tokens"].(map[string]any)
	if !ok {
		return
	}
	var cacheRead, cacheWrite any
	if cache, ok := tokens["cache"].(map[string]any); ok {
		cacheRead = cache["read"]
		cacheWrite = cache["write"]
	}
	a.addCounters(tokens["input"], tokens["output"], cacheRead, cacheWrite)
	if reasoning, ok := count(tokens["reasoning"]); ok && a.reported {
		a.output = a.add(a.output, reasoning)
	}
}

func (a *UsageAccumulator) observeModel(value any) {
	model, ok := value.(string)
	if ok && usageModelPattern.MatchString(model) {
		a.model = model
	}
}

func (a *UsageAccumulator) addCounters(input, output, cacheRead, cacheWrite any) {
	inputCount, inputOK := count(input)
	outputCount, outputOK := count(output)
	if !inputOK || !outputOK {
		return
	}
	a.reported = true
	a.input = a.add(a.input, inputCount)
	a.output = a.add(a.output, outputCount)
	if value, ok := count(cacheRead); ok {
		a.cacheRead = a.addOptional(a.cacheRead, value)
	}
	if value, ok := count(cacheWrite); ok {
		a.cacheWrite = a.addOptional(a.cacheWrite, value)
	}
}

func (a *UsageAccumulator) add(total, value uint64) uint64 {
	if value > v1.ProviderUsageMaxCount-total {
		a.overflow = true
		return total
	}
	return total + value
}

func (a *UsageAccumulator) addOptional(total *uint64, value uint64) *uint64 {
	next := a.add(valueOrZero(total), value)
	return &next
}

func valueOrZero(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func count(value any) (uint64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number != math.Trunc(number) || number > float64(v1.ProviderUsageMaxCount) {
		return 0, false
	}
	return uint64(number), true
}
