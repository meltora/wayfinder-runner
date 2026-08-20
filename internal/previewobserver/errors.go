package previewobserver

import (
	"errors"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type observationError struct {
	code v1.PreviewObservationErrorCode
}

func (err observationError) Error() string {
	return "preview observation failed: " + string(err.code)
}

func resultError(code v1.PreviewObservationErrorCode) error {
	return observationError{code: code}
}

func resultCode(err error) v1.PreviewObservationErrorCode {
	var observed observationError
	if errors.As(err, &observed) {
		return observed.code
	}
	return v1.PreviewObserverUnavailable
}
