package provider

import (
	"strconv"
	"strings"
	"time"
)

type CapacityReason string

const CapacityReasonQuotaExhausted CapacityReason = "quota_exhausted"

type CapacityFailure struct {
	Reason  CapacityReason
	RetryAt time.Time
}

const maxProviderResetHorizon = 31 * 24 * time.Hour

// ClassifyCapacityFailure normalizes subscription and credit exhaustion from
// provider JSON streams. The classifier deliberately does not treat a generic
// HTTP 429 as exhausted capacity because short-lived rate limiting should use
// ordinary retry handling instead of waiting for a billing reset.
func ClassifyCapacityFailure(adapter string, event map[string]any, now time.Time) (CapacityFailure, bool) {
	_ = adapter // Reserved for adapter-specific structured fields as CLIs evolve.
	message := strings.ToLower(ErrorMessage(event))
	if message == "" || !quotaMessage(message) {
		return CapacityFailure{}, false
	}
	failure := CapacityFailure{Reason: CapacityReasonQuotaExhausted}
	if retryAt, ok := retryTime(event, now); ok {
		failure.RetryAt = retryAt
	}
	return failure, true
}

func ProviderIdentity(adapter, model string) string {
	providerName, _, ok := strings.Cut(model, "/")
	if ok && providerName != "" {
		return providerName
	}
	return adapter
}

// ErrorMessage extracts only provider-declared error output. Ordinary model
// text is never interpreted as an operational failure.
func ErrorMessage(event map[string]any) string {
	if !isErrorEvent(event) {
		return ""
	}
	if nested, ok := event["error"]; ok {
		if message := findMessage(nested, 0); message != "" {
			return message
		}
	}
	for _, key := range []string{"message", "result"} {
		if message, ok := event[key].(string); ok {
			return message
		}
	}
	return ""
}

func isErrorEvent(event map[string]any) bool {
	eventType, _ := event["type"].(string)
	if strings.EqualFold(eventType, "error") {
		return true
	}
	if isError, _ := event["is_error"].(bool); isError {
		return true
	}
	if _, ok := event["error"]; ok && (strings.Contains(strings.ToLower(eventType), "fail") || event["status"] == "failed") {
		return true
	}
	return false
}

func findMessage(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		for _, key := range []string{"message", "detail", "reason"} {
			if message, ok := typed[key].(string); ok && message != "" {
				return message
			}
		}
		for _, key := range []string{"data", "error", "cause"} {
			if nested, ok := typed[key]; ok {
				if message := findMessage(nested, depth+1); message != "" {
					return message
				}
			}
		}
		if name, ok := typed["name"].(string); ok {
			return name
		}
	}
	return ""
}

func quotaMessage(message string) bool {
	for _, phrase := range []string{
		"quota exhausted",
		"quota exceeded",
		"usage limit",
		"hit your limit",
		"billing-cycle quota",
		"billing cycle quota",
		"monthly limit",
		"weekly limit",
		"daily limit",
		"insufficient credits",
		"out of credits",
		"credit balance",
		"weighted tokens left",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func retryTime(event map[string]any, now time.Time) (time.Time, bool) {
	for _, key := range []string{"resetat", "resetsat", "nextresetat", "retryat"} {
		if value, ok := findRetryValue(event, key, 0); ok {
			if retryAt, ok := parseRetryTime(value); ok && validRetryTime(retryAt, now) {
				return retryAt, true
			}
		}
	}
	for _, key := range []string{"retryafterseconds", "retryafter"} {
		if value, ok := findRetryValue(event, key, 0); ok {
			if delay, ok := parseRetryDelay(value); ok {
				retryAt := now.Add(delay)
				if validRetryTime(retryAt, now) {
					return retryAt, true
				}
			}
		}
	}
	return time.Time{}, false
}

func findRetryValue(value any, accepted string, depth int) (any, bool) {
	if depth > 5 {
		return nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			if normalized == accepted {
				return nested, true
			}
		}
		for _, nested := range typed {
			if found, ok := findRetryValue(nested, accepted, depth+1); ok {
				return found, true
			}
		}
	case []any:
		for _, nested := range typed {
			if found, ok := findRetryValue(nested, accepted, depth+1); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func parseRetryTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			return parsed.UTC(), true
		}
		if numeric, err := strconv.ParseFloat(typed, 64); err == nil {
			return unixTimestamp(numeric), true
		}
	case float64:
		return unixTimestamp(typed), true
	case int:
		return unixTimestamp(float64(typed)), true
	case int64:
		return unixTimestamp(float64(typed)), true
	}
	return time.Time{}, false
}

func unixTimestamp(value float64) time.Time {
	if value > 1e12 {
		return time.UnixMilli(int64(value)).UTC()
	}
	return time.Unix(int64(value), 0).UTC()
}

func parseRetryDelay(value any) (time.Duration, bool) {
	var seconds float64
	switch typed := value.(type) {
	case float64:
		seconds = typed
	case int:
		seconds = float64(typed)
	case int64:
		seconds = float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, false
		}
		seconds = parsed
	default:
		return 0, false
	}
	if seconds <= 0 || seconds > maxProviderResetHorizon.Seconds() {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func validRetryTime(retryAt, now time.Time) bool {
	return retryAt.After(now.Add(-24*time.Hour)) && retryAt.Before(now.Add(maxProviderResetHorizon))
}
