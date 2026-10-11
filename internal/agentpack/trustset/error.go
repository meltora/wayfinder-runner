package trustset

import (
	"errors"
	"fmt"
)

const (
	CodeTooLarge              = "too_large"
	CodeMalformed             = "malformed"
	CodeNonCanonical          = "non_canonical"
	CodeUnsupportedFormat     = "unsupported_format"
	CodeInvalidSequence       = "invalid_sequence"
	CodeInvalidTimestamp      = "invalid_timestamp"
	CodeTooManyKeys           = "too_many_keys"
	CodeTooManyRevokedKeyIDs  = "too_many_revoked_key_ids"
	CodeInvalidKeyID          = "invalid_key_id"
	CodeInvalidPublicKey      = "invalid_public_key"
	CodeInvalidValidity       = "invalid_validity_window"
	CodeDuplicateKeyID        = "duplicate_key_id"
	CodeKeyIDConflict         = "key_id_conflict"
	CodeDuplicatePublicKey    = "duplicate_public_key"
	CodeDuplicateRevokedKeyID = "duplicate_revoked_key_id"
	CodeKeyListedAndRevoked   = "key_listed_and_revoked"
	CodeUnknownRoot           = "unknown_root"
	CodeSignatureInvalid      = "signature_invalid"
	CodeInvalidEnvelope       = "invalid_envelope"
)

type Error struct {
	Code   string
	Detail string
}

func (failure *Error) Error() string {
	return fmt.Sprintf("trusted key set %s: %s", failure.Code, failure.Detail)
}

func refuse(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func CodeOf(err error) string {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}
