package main

import (
	"fmt"
	"io"
)

type codedError struct {
	code    string
	message string
}

func (failure codedError) Error() string { return failure.code + ": " + failure.message }

func refused(code, format string, args ...any) error {
	return codedError{code: code, message: fmt.Sprintf(format, args...)}
}

type jsonErrorBody struct {
	Error jsonErrorDetail `json:"error"`
}

type jsonErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSONError(stderr io.Writer, failure codedError) {
	_ = writeJSON(stderr, jsonErrorBody{Error: jsonErrorDetail{Code: failure.code, Message: failure.message}})
}
