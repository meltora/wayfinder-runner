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
	invalid    bool
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
	if !a.reported || a.invalid {
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
		usage, present, ok := optionalObject(event["usage"])
		if !ok {
			a.invalid = true
			return
		}
		if !present {
			return
		}
		a.addCounters(
			usage["input_tokens"],
			usage["output_tokens"],
			usage["cache_read_input_tokens"],
			usage["cache_creation_input_tokens"],
			nil,
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
		usage, present, ok := optionalObject(event["usage"])
		if !ok {
			a.invalid = true
			return
		}
		if !present {
			return
		}
		a.addCounters(usage["input_tokens"], usage["output_tokens"], usage["cached_input_tokens"], nil, nil)
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
	tokens, present, ok := optionalObject(part["tokens"])
	if !ok {
		a.invalid = true
		return
	}
	if !present {
		return
	}
	cache, _, ok := optionalObject(tokens["cache"])
	if !ok {
		a.invalid = true
		return
	}
	a.addCounters(tokens["input"], tokens["output"], cache["read"], cache["write"], tokens["reasoning"])
}

func optionalObject(value any) (map[string]any, bool, bool) {
	if value == nil {
		return nil, false, true
	}
	object, ok := value.(map[string]any)
	return object, ok, ok
}

func (a *UsageAccumulator) observeModel(value any) {
	model, ok := value.(string)
	if ok && usageModelPattern.MatchString(model) {
		a.model = model
	}
}

func (a *UsageAccumulator) addCounters(input, output, cacheRead, cacheWrite, extraOutput any) {
	inputCount, inputOK := count(input)
	outputCount, outputOK := count(output)
	readCount, readPresent, readOK := optionalCount(cacheRead)
	writeCount, writePresent, writeOK := optionalCount(cacheWrite)
	extraCount, _, extraOK := optionalCount(extraOutput)
	if !inputOK || !outputOK || !readOK || !writeOK || !extraOK {
		a.invalid = true
		return
	}
	a.reported = true
	a.input = a.add(a.input, inputCount)
	a.output = a.add(a.add(a.output, outputCount), extraCount)
	if readPresent {
		a.cacheRead = a.addOptional(a.cacheRead, readCount)
	}
	if writePresent {
		a.cacheWrite = a.addOptional(a.cacheWrite, writeCount)
	}
}

func optionalCount(value any) (uint64, bool, bool) {
	if value == nil {
		return 0, false, true
	}
	number, ok := count(value)
	return number, ok, ok
}

func (a *UsageAccumulator) add(total, value uint64) uint64 {
	if value > v1.ProviderUsageMaxCount-total {
		a.invalid = true
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
