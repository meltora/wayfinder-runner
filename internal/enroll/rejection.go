package enroll

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type RejectedError struct {
	Status     int
	Code       string
	RetryAfter int
	body       string
}

var safeRejectionCode = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

var rejectionKinds = map[string]string{
	"enrollment_code_rejected":      "rejected",
	"enrollment_code_expired":       "expired",
	"enrollment_code_used":          "used",
	"enrollment_code_revoked":       "revoked",
	"RUNNER_ENROLLMENT_DENIED":      "rejected",
	"RUNNER_ENROLLMENT_NOT_FOUND":   "rejected",
	"enrollment_code_malformed":     "malformed",
	"enrollment_request_invalid":    "invalid",
	"VALIDATION":                    "invalid",
	"RUNNER_ENROLLMENT_INVALID_CSR": "invalid",
	"PAYLOAD_TOO_LARGE":             "invalid",
	"UNSUPPORTED_MEDIA_TYPE":        "invalid",
	"enrollment_rate_limited":       "rate_limited",
	"TOO_MANY_REQUESTS":             "rate_limited",
	"enrollment_unavailable":        "unavailable",
	"RUNNER_ENROLLMENT_UNAVAILABLE": "unavailable",
	"SERVICE_UNAVAILABLE":           "unavailable",
}

func newRejectedError(resp *http.Response, body []byte) *RejectedError {
	rejected := &RejectedError{Status: resp.StatusCode, body: strings.Map(dropControl, truncateResponse(body))}
	var wire struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &wire) == nil && safeRejectionCode.MatchString(wire.Code) {
		rejected.Code = wire.Code
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 3600 {
		rejected.RetryAfter = seconds
	}
	return rejected
}

func dropControl(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}

func (e *RejectedError) Error() string {
	switch rejectionKinds[e.Code] {
	case "rejected":
		return "enrollment code was not accepted: it may be mistyped, expired, already used or revoked; create a new code in the dashboard and run enroll again"
	case "expired":
		return "this enrollment code has expired; create a new code in the dashboard and run enroll again"
	case "used":
		return "this enrollment code was already used by another runner; create a new code in the dashboard and run enroll again"
	case "revoked":
		return "this enrollment code was revoked in the dashboard; create a new code and run enroll again"
	case "malformed":
		return "that is not an enrollment code: copy the whole code from the dashboard (it starts with ren1.)"
	case "invalid":
		return fmt.Sprintf("the control plane refused this runner's enrollment request (status %d); check that this runner version is supported by the control plane", e.Status)
	case "rate_limited":
		if e.RetryAfter > 0 {
			return fmt.Sprintf("too many enrollment attempts from this address; wait %d seconds and run enroll again", e.RetryAfter)
		}
		return "too many enrollment attempts from this address; wait a minute and run enroll again"
	case "unavailable":
		return "runner enrollment is not available on this control plane right now; try again later, and check --url if it keeps failing"
	}
	return fmt.Sprintf("enroll rejected: status %d: %s", e.Status, e.body)
}
