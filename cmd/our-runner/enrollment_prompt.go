package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/term"
)

const (
	enrollmentPromptText     = "Paste or type the enrollment code and press Enter: "
	enrollmentPromptRetry    = "That does not look like an enrollment code (it starts with ren1.); try again."
	enrollmentPromptMask     = "•"
	enrollmentPromptReceived = "Enrollment code received: ••••…"
	enrollmentPromptAttempts = 3
	enrollmentCodeMaxLength  = 4096
	enrollmentPromptNoCode   = "No enrollment code was entered."
	enrollmentPromptCancel   = "Enrollment cancelled."
)

var enrollmentCodePattern = regexp.MustCompile(`^ren1\.[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.[A-Za-z0-9_-]{43}$`)

var errEnrollmentNeedsTerminal = errors.New("enroll needs a terminal to ask for the code; pass --code-file FILE or --code-file - to read it from a file or stdin")

type enrollmentTerminal struct {
	isTerminal bool
	makeRaw    func() (func() error, error)
	input      io.Reader
	output     io.Writer
	signals    func() (<-chan os.Signal, func())
}

type commandExitError struct {
	code    int
	message string
}

func (e *commandExitError) Error() string { return e.message }

func reportCommandError(output io.Writer, err error) int {
	var exit *commandExitError
	if errors.As(err, &exit) {
		fmt.Fprintln(output, exit.message)
		return exit.code
	}
	fmt.Fprintln(output, "error:", err)
	return 1
}

func osEnrollmentTerminal() enrollmentTerminal {
	fd := int(os.Stdin.Fd())
	return enrollmentTerminal{
		isTerminal: term.IsTerminal(fd),
		makeRaw: func() (func() error, error) {
			state, err := term.MakeRaw(fd)
			if err != nil {
				return nil, err
			}
			return func() error { return term.Restore(fd, state) }, nil
		},
		input:  os.Stdin,
		output: os.Stderr,
		signals: func() (<-chan os.Signal, func()) {
			interrupts := make(chan os.Signal, 1)
			signal.Notify(interrupts, syscall.SIGINT, syscall.SIGTERM)
			return interrupts, func() { signal.Stop(interrupts) }
		},
	}
}

func promptEnrollmentCode(terminal enrollmentTerminal) (string, error) {
	interrupts, stopSignals := terminal.signals()
	defer stopSignals()
	restore, err := terminal.makeRaw()
	if err != nil {
		return "", fmt.Errorf("prepare the terminal for the enrollment code: %w", err)
	}
	defer func() { _ = restore() }()
	keys := &enrollmentKeyReader{input: terminal.input, interrupts: interrupts}
	for attempt := 0; attempt < enrollmentPromptAttempts; attempt++ {
		fmt.Fprint(terminal.output, enrollmentPromptText)
		entry, err := readMaskedEnrollmentEntry(keys, terminal.output)
		fmt.Fprint(terminal.output, "\r\n")
		if err != nil {
			return "", err
		}
		code := strings.TrimSpace(entry)
		if code == "" {
			return "", &commandExitError{code: 1, message: enrollmentPromptNoCode}
		}
		fmt.Fprintf(terminal.output, "%s%s\r\n", enrollmentPromptReceived, lastEnrollmentCodeCharacters(code))
		if enrollmentCodePattern.MatchString(code) {
			return code, nil
		}
		fmt.Fprintf(terminal.output, "%s\r\n", enrollmentPromptRetry)
	}
	return "", fmt.Errorf("no valid enrollment code after %d attempts", enrollmentPromptAttempts)
}

func lastEnrollmentCodeCharacters(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[len(code)-4:]
}

func readMaskedEnrollmentEntry(keys *enrollmentKeyReader, output io.Writer) (string, error) {
	entry := make([]byte, 0, 128)
	for {
		key, err := keys.next()
		if errors.Is(err, io.EOF) {
			return "", &commandExitError{code: 1, message: enrollmentPromptNoCode}
		}
		if err != nil {
			return "", err
		}
		switch {
		case key == '\r' || key == '\n':
			return string(entry), nil
		case key == 0x03:
			return "", &commandExitError{code: 130, message: enrollmentPromptCancel}
		case key == 0x04:
			if len(entry) == 0 {
				return "", &commandExitError{code: 1, message: enrollmentPromptNoCode}
			}
		case key == 0x7f || key == 0x08:
			if len(entry) > 0 {
				entry = entry[:len(entry)-1]
				fmt.Fprint(output, "\b \b")
			}
		case key == 0x1b:
			keys.skipEscapeSequence()
		case key >= 0x20 && key <= 0x7e:
			if len(entry) < enrollmentCodeMaxLength {
				entry = append(entry, key)
				fmt.Fprint(output, enrollmentPromptMask)
			}
		}
	}
}

type enrollmentKeyReader struct {
	input      io.Reader
	interrupts <-chan os.Signal
	pending    []byte
	err        error
}

type enrollmentRead struct {
	data     []byte
	err      error
	panicked bool
	value    any
}

func (r *enrollmentKeyReader) next() (byte, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		results := make(chan enrollmentRead, 1)
		go r.read(results)
		select {
		case result := <-results:
			if result.panicked {
				panic(result.value)
			}
			r.pending, r.err = result.data, result.err
		case received := <-r.interrupts:
			return 0, enrollmentSignalExit(received)
		}
	}
	key := r.pending[0]
	r.pending = r.pending[1:]
	return key, nil
}

func (r *enrollmentKeyReader) read(results chan<- enrollmentRead) {
	result := enrollmentRead{}
	defer func() {
		if value := recover(); value != nil {
			result = enrollmentRead{panicked: true, value: value}
		}
		results <- result
	}()
	buffer := make([]byte, 512)
	n, err := r.input.Read(buffer)
	result = enrollmentRead{data: buffer[:n], err: err}
}

func (r *enrollmentKeyReader) skipEscapeSequence() {
	if len(r.pending) == 0 {
		return
	}
	switch introducer := r.pending[0]; {
	case introducer == '[':
		r.pending = r.pending[1:]
		for len(r.pending) > 0 && r.pending[0] >= 0x20 && r.pending[0] <= 0x3f {
			r.pending = r.pending[1:]
		}
		if len(r.pending) > 0 && r.pending[0] >= 0x40 && r.pending[0] <= 0x7e {
			r.pending = r.pending[1:]
		}
	case introducer == 'O':
		r.pending = r.pending[1:]
		if len(r.pending) > 0 {
			r.pending = r.pending[1:]
		}
	case introducer >= 0x20 && introducer <= 0x7e:
		r.pending = r.pending[1:]
	}
}

func enrollmentSignalExit(received os.Signal) error {
	code := 130
	if number, ok := received.(syscall.Signal); ok {
		code = 128 + int(number)
	}
	return &commandExitError{code: code, message: enrollmentPromptCancel}
}
