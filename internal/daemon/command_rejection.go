package daemon

import (
	"errors"
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type commandRejection struct {
	reasonCode v1.CommandAckReasonCode
	cause      error
}

func (rejection *commandRejection) Error() string {
	return fmt.Sprintf("command rejected (%s)", rejection.reasonCode)
}

func (rejection *commandRejection) Unwrap() error { return rejection.cause }

func rejectCommand(reasonCode v1.CommandAckReasonCode, cause error) error {
	if !v1.IsCommandAckReasonCode(reasonCode) {
		reasonCode = v1.CommandAckReasonInternalRejection
	}
	return &commandRejection{reasonCode: reasonCode, cause: cause}
}

func commandRejectionReason(err error) v1.CommandAckReasonCode {
	if err == nil {
		return ""
	}
	var rejection *commandRejection
	if errors.As(err, &rejection) && v1.IsCommandAckReasonCode(rejection.reasonCode) {
		return rejection.reasonCode
	}
	return v1.CommandAckReasonInternalRejection
}
