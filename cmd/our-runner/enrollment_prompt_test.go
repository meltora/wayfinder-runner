package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

const (
	promptTestCode      = "ren1.0f8fad5b-d9cb-469f-a165-70867728950e.QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQx9_Z"
	promptTestCodeUUID  = "0f8fad5b-d9cb-469f-a165-70867728950e"
	promptTestPrompt    = "Paste or type the enrollment code and press Enter: "
	promptTestReceived  = "Enrollment code received: ••••…"
	promptTestRetryText = "That does not look like an enrollment code (it starts with ren1.); try again."
)

type fakeEnrollmentTerminal struct {
	input       io.Reader
	output      bytes.Buffer
	terminal    bool
	rawCalls    int
	restores    int
	rawErr      error
	interrupts  chan os.Signal
	signalStops int
}

func newFakeEnrollmentTerminal(input string) *fakeEnrollmentTerminal {
	return &fakeEnrollmentTerminal{input: strings.NewReader(input), terminal: true, interrupts: make(chan os.Signal, 1)}
}

func (f *fakeEnrollmentTerminal) terminalFor() enrollmentTerminal {
	return enrollmentTerminal{
		isTerminal: f.terminal,
		makeRaw: func() (func() error, error) {
			f.rawCalls++
			if f.rawErr != nil {
				return nil, f.rawErr
			}
			return func() error {
				f.restores++
				return nil
			}, nil
		},
		input:  f.input,
		output: &f.output,
		signals: func() (<-chan os.Signal, func()) {
			return f.interrupts, func() { f.signalStops++ }
		},
	}
}

type countingReader struct {
	reads int
}

func (r *countingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) {
	panic("reader exploded")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("device went away")
}

func startPromptControlPlane(t *testing.T) *mockcp.Server {
	t.Helper()
	server, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	server.AddEnrollmentCode(promptTestCode)
	return server
}

func promptEnrollArgs(stateDir, url string) []string {
	return []string{"--state-dir", stateDir, "--url", url, "--allow-insecure"}
}

func assertNoFullCode(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if strings.Contains(text, promptTestCode) || strings.Contains(text, promptTestCodeUUID) || strings.Contains(text, "QQQQ") {
			t.Fatalf("enrollment code leaked into %q", text)
		}
	}
}

func assertStateDirUntouched(t *testing.T, stateDir string) {
	t.Helper()
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory was written: %v", err)
	}
}

func typedMasks(output string) int {
	typed, _, _ := strings.Cut(output, "\r\n")
	return strings.Count(typed, "•")
}

func assertExit(t *testing.T, err error, code int, message string) {
	t.Helper()
	var exit *commandExitError
	if !errors.As(err, &exit) {
		t.Fatalf("error = %v, want exit %d", err, code)
	}
	if exit.code != code || exit.message != message {
		t.Fatalf("exit = %d %q, want %d %q", exit.code, exit.message, code, message)
	}
}

func TestEnrollPromptMasksTypedCodeAndEnrolls(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal(promptTestCode + "\r")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	want := promptTestPrompt + strings.Repeat("•", len(promptTestCode)) + "\r\n" + promptTestReceived + "x9_Z\r\n"
	if fake.output.String() != want {
		t.Fatalf("output = %q, want %q", fake.output.String(), want)
	}
	if id, err := daemon.ReadRunnerID(stateDir); err != nil || id == "" {
		t.Fatalf("runner id = %q, err = %v", id, err)
	}
	if fake.rawCalls != 1 || fake.restores != 1 || fake.signalStops != 1 {
		t.Fatalf("raw = %d, restores = %d, signal stops = %d", fake.rawCalls, fake.restores, fake.signalStops)
	}
	assertNoFullCode(t, fake.output.String())
}

func TestEnrollPromptTrimsPastedCodeAndIgnoresBracketedPasteMarkers(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal("\x1b[200~  " + promptTestCode + " \x1b[201~\n")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if got := typedMasks(fake.output.String()); got != len(promptTestCode)+3 {
		t.Fatalf("masks = %d, want %d", got, len(promptTestCode)+3)
	}
	if !strings.HasSuffix(fake.output.String(), promptTestReceived+"x9_Z\r\n") {
		t.Fatalf("output = %q", fake.output.String())
	}
	assertNoFullCode(t, fake.output.String())
}

func TestEnrollPromptBackspaceRemovesOneCharacterAndOneMask(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal(promptTestCode + "zz\x7f\x08\r")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	want := promptTestPrompt + strings.Repeat("•", len(promptTestCode)+2) + "\b \b\b \b\r\n" + promptTestReceived + "x9_Z\r\n"
	if fake.output.String() != want {
		t.Fatalf("output = %q, want %q", fake.output.String(), want)
	}
}

func TestEnrollPromptBackspaceOnEmptyEntryShowsNothing(t *testing.T) {
	fake := newFakeEnrollmentTerminal("\x7f\x08\x03")
	stateDir := filepath.Join(t.TempDir(), "state")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	assertExit(t, err, 130, "Enrollment cancelled.")
	if fake.output.String() != promptTestPrompt+"\r\n" {
		t.Fatalf("output = %q", fake.output.String())
	}
}

func TestEnrollPromptIgnoresNonASCIIControlAndEscapeSequences(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	half := len(promptTestCode) / 2
	noise := "é\x1b[A\x1bOB\x1b[1;5C\t\x00\x1bxü€"
	fake := newFakeEnrollmentTerminal(promptTestCode[:half] + noise + promptTestCode[half:] + "\r")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if got := typedMasks(fake.output.String()); got != len(promptTestCode) {
		t.Fatalf("masks = %d, want %d", got, len(promptTestCode))
	}
}

func TestEnrollPromptRetriesAfterInvalidFormat(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal("hello-world\r" + promptTestCode + "\r")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	out := fake.output.String()
	if strings.Count(out, promptTestPrompt) != 2 || strings.Count(out, promptTestRetryText) != 1 {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, promptTestReceived+"orld\r\n"+promptTestRetryText+"\r\n"+promptTestPrompt) {
		t.Fatalf("output = %q", out)
	}
	if fake.restores != 1 {
		t.Fatalf("restores = %d", fake.restores)
	}
}

func TestEnrollPromptStopsAfterThreeInvalidEntries(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal("one-bad-code\rtwo-bad-code\r" + strings.Replace(promptTestCode, "ren1.", "ren2.", 1) + "\r" + promptTestCode + "\r")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	if err == nil || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("error = %v", err)
	}
	if strings.Count(fake.output.String(), promptTestRetryText) != 3 || strings.Count(fake.output.String(), promptTestPrompt) != 3 {
		t.Fatalf("output = %q", fake.output.String())
	}
	if fake.restores != 1 {
		t.Fatalf("restores = %d", fake.restores)
	}
	assertStateDirUntouched(t, stateDir)
	assertNoFullCode(t, fake.output.String(), err.Error())
}

func TestEnrollPromptWithoutEntryExitsWithMessage(t *testing.T) {
	for name, input := range map[string]string{
		"empty enter":       "\r",
		"spaces only":       "   \r",
		"ctrl-d empty line": "\x04",
		"eof":               "",
		"eof after typing":  "ren1.",
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			fake := newFakeEnrollmentTerminal(input)
			err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
			assertExit(t, err, 1, "No enrollment code was entered.")
			if fake.restores != 1 || fake.signalStops != 1 {
				t.Fatalf("restores = %d, signal stops = %d", fake.restores, fake.signalStops)
			}
			assertStateDirUntouched(t, stateDir)
		})
	}
}

func TestEnrollPromptCtrlDAfterTypingIsIgnored(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal(promptTestCode[:10] + "\x04" + promptTestCode[10:] + "\r")

	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
}

func TestEnrollPromptCtrlCCancels(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal(promptTestCode[:20] + "\x03" + promptTestCode[20:] + "\r")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	assertExit(t, err, 130, "Enrollment cancelled.")
	if fake.restores != 1 {
		t.Fatalf("restores = %d", fake.restores)
	}
	if strings.Contains(fake.output.String(), promptTestReceived) {
		t.Fatalf("output = %q", fake.output.String())
	}
	assertStateDirUntouched(t, stateDir)
	assertNoFullCode(t, fake.output.String(), err.Error())
}

func TestEnrollPromptSignalsRestoreTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		signal os.Signal
		code   int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run(tc.signal.String(), func(t *testing.T) {
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = writer.Close() })
			stateDir := filepath.Join(t.TempDir(), "state")
			fake := newFakeEnrollmentTerminal("")
			fake.input = reader
			fake.interrupts <- tc.signal

			err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
			assertExit(t, err, tc.code, "Enrollment cancelled.")
			if fake.restores != 1 || fake.signalStops != 1 {
				t.Fatalf("restores = %d, signal stops = %d", fake.restores, fake.signalStops)
			}
			assertStateDirUntouched(t, stateDir)
		})
	}
}

func TestEnrollPromptRestoresTheTerminalWhenTheReaderPanics(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal("")
	fake.input = panickingReader{}

	func() {
		defer func() {
			if recovered := recover(); recovered != "reader exploded" {
				t.Fatalf("recovered = %v", recovered)
			}
		}()
		_ = cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
		t.Fatal("panic was swallowed")
	}()
	if fake.restores != 1 || fake.signalStops != 1 {
		t.Fatalf("restores = %d, signal stops = %d", fake.restores, fake.signalStops)
	}
	assertStateDirUntouched(t, stateDir)
}

func TestEnrollPromptRestoresTheTerminalAfterAReadError(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal("")
	fake.input = io.MultiReader(strings.NewReader(promptTestCode[:30]), failingReader{})

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	if err == nil || !strings.Contains(err.Error(), "device went away") {
		t.Fatalf("error = %v", err)
	}
	if fake.restores != 1 {
		t.Fatalf("restores = %d", fake.restores)
	}
	assertStateDirUntouched(t, stateDir)
	assertNoFullCode(t, err.Error())
}

func TestEnrollPromptCapsInputAt4096Characters(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	fake := newFakeEnrollmentTerminal(strings.Repeat("a", 5000) + "\x03")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	assertExit(t, err, 130, "Enrollment cancelled.")
	if got := typedMasks(fake.output.String()); got != 4096 {
		t.Fatalf("masks = %d", got)
	}
}

func TestEnrollPromptFailsWhenTheTerminalCannotBeMadeRaw(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	reader := &countingReader{}
	fake := newFakeEnrollmentTerminal("")
	fake.input = reader
	fake.rawErr = errors.New("not a tty")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	if err == nil || !strings.Contains(err.Error(), "not a tty") {
		t.Fatalf("error = %v", err)
	}
	if reader.reads != 0 || fake.restores != 0 {
		t.Fatalf("reads = %d, restores = %d", reader.reads, fake.restores)
	}
	assertStateDirUntouched(t, stateDir)
}

func TestEnrollWithoutTerminalOrCodeFileReadsNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	reader := &countingReader{}
	fake := newFakeEnrollmentTerminal("")
	fake.input = reader
	fake.terminal = false

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, "https://must-not-contact.invalid"), fake.terminalFor())
	want := "enroll needs a terminal to ask for the code; pass --code-file FILE or --code-file - to read it from a file or stdin"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v", err)
	}
	if reader.reads != 0 || fake.rawCalls != 0 || fake.output.Len() != 0 {
		t.Fatalf("reads = %d, raw = %d, output = %q", reader.reads, fake.rawCalls, fake.output.String())
	}
	assertStateDirUntouched(t, stateDir)
}

func TestEnrollWithCodeFileNeverPrompts(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	codeFile := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(codeFile, []byte(promptTestCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := &countingReader{}
	fake := newFakeEnrollmentTerminal("")
	fake.input = reader

	if err := cmdEnrollWithTerminal(append(promptEnrollArgs(stateDir, server.EnrollURL()), "--code-file", codeFile), fake.terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if reader.reads != 0 || fake.rawCalls != 0 || fake.output.Len() != 0 {
		t.Fatalf("reads = %d, raw = %d, output = %q", reader.reads, fake.rawCalls, fake.output.String())
	}
}

func TestEnrollPromptIsNotShownForAnEnrolledStateDirectory(t *testing.T) {
	server := startPromptControlPlane(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), newFakeEnrollmentTerminal(promptTestCode+"\r").terminalFor()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	fake := newFakeEnrollmentTerminal(promptTestCode + "\r")

	err := cmdEnrollWithTerminal(promptEnrollArgs(stateDir, server.EnrollURL()), fake.terminalFor())
	if err == nil || !strings.Contains(err.Error(), "already enrolled") {
		t.Fatalf("error = %v", err)
	}
	if fake.rawCalls != 0 || fake.output.Len() != 0 {
		t.Fatalf("raw = %d, output = %q", fake.rawCalls, fake.output.String())
	}
}

func TestReportCommandErrorUsesExitCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		output string
	}{
		{&commandExitError{code: 130, message: "Enrollment cancelled."}, 130, "Enrollment cancelled.\n"},
		{&commandExitError{code: 1, message: "No enrollment code was entered."}, 1, "No enrollment code was entered.\n"},
		{errors.New("boom"), 1, "error: boom\n"},
	} {
		var output bytes.Buffer
		if code := reportCommandError(&output, tc.err); code != tc.code || output.String() != tc.output {
			t.Fatalf("code = %d, output = %q", code, output.String())
		}
	}
}
