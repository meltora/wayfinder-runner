package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

type usageError struct{ message string }

func (failure usageError) Error() string { return failure.message }

type command func(args []string, stdout io.Writer, stderr io.Writer) error

var commands = map[string]command{
	"keygen":     runKeygen,
	"public-key": runPublicKey,
	"build":      runBuild,
	"digest":     runDigest,
	"prove":      runProve,
	"sign":       runSign,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: agentpack keygen|public-key|build|digest|prove|sign [flags]")
		return exitUsage
	}
	handler, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "agentpack: unknown command %q\n", args[0])
		return exitUsage
	}
	if err := handler(args[1:], stdout, stderr); err != nil {
		var usage usageError
		if errors.As(err, &usage) || errors.Is(err, flag.ErrHelp) {
			if usage.message != "" {
				_, _ = fmt.Fprintf(stderr, "agentpack %s: %s\n", args[0], usage.message)
			}
			return exitUsage
		}
		_, _ = fmt.Fprintf(stderr, "agentpack %s: %s\n", args[0], err)
		return exitFailure
	}
	return exitOK
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("agentpack "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string, positional int) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{}
	}
	if flags.NArg() != positional {
		return usageError{message: fmt.Sprintf("expected %d positional arguments, got %d", positional, flags.NArg())}
	}
	return nil
}

func requireFlags(values map[string]string) error {
	for name, value := range values {
		if value == "" {
			return usageError{message: "--" + name + " is required"}
		}
	}
	return nil
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
