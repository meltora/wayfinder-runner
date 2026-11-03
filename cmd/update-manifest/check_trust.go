package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/updater"
)

const maxTrustBytes = 256 << 10

func runCheckTrust(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("check-trust", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	allowEmpty := flags.Bool("allow-empty", false, "accept an unprovisioned trust file with no keys")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return fmt.Errorf("usage: update-manifest check-trust [--allow-empty] FILE")
	}
	raw, err := safeio.ReadRegularFile(flags.Arg(0), maxTrustBytes, 0o002)
	if err != nil {
		return err
	}
	trust, err := updater.ParseTrustDocument(raw)
	if errors.Is(err, updater.ErrTrustEmpty) && *allowEmpty {
		_, _ = fmt.Fprintln(stdout, "update trust: empty (allowed for this channel; automatic updates stay disabled)")
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	active := 0
	for _, key := range trust.Keys {
		if now.Before(key.NotBefore) || !now.Before(key.NotAfter) || (key.RevokedAt != nil && !now.Before(*key.RevokedAt)) {
			continue
		}
		active++
	}
	if active < trust.Threshold {
		return fmt.Errorf("update trust has %d currently valid keys, fewer than the signature threshold %d", active, trust.Threshold)
	}
	_, _ = fmt.Fprintf(stdout, "update trust: %d keys, threshold %d, %d currently valid\n", len(trust.Keys), trust.Threshold, active)
	return nil
}
