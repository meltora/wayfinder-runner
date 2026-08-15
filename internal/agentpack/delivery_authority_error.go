package agentpack

import "errors"

type DeliveryAuthorityFailureKind string

const (
	DeliveryAuthorityInvalid      DeliveryAuthorityFailureKind = "invalid"
	DeliveryAuthorityIncompatible DeliveryAuthorityFailureKind = "incompatible"
)

type DeliveryAuthorityError struct {
	kind  DeliveryAuthorityFailureKind
	cause error
}

func (failure *DeliveryAuthorityError) Error() string {
	if failure.kind == DeliveryAuthorityIncompatible {
		return "Agent Pack delivery authority is incompatible with this runner"
	}
	return "Agent Pack delivery authority is invalid"
}

func (failure *DeliveryAuthorityError) Unwrap() error { return failure.cause }

func newDeliveryAuthorityError(kind DeliveryAuthorityFailureKind, cause error) error {
	return &DeliveryAuthorityError{kind: kind, cause: cause}
}

func DeliveryAuthorityFailureKindOf(err error) (DeliveryAuthorityFailureKind, bool) {
	var failure *DeliveryAuthorityError
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.kind, true
}
