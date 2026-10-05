package main

import (
	"io"

	"github.com/example/app-runner/internal/agentpack"
)

func runKeygen(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("keygen", stderr)
	out := flags.String("out", "", "path of the new private key file (0600, never overwritten)")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"out": *out}); err != nil {
		return err
	}
	signer, err := agentpack.GenerateFileSigningKey(*out)
	if err != nil {
		return err
	}
	identity, err := agentpack.SignerPublicKeyIdentity(signer)
	if err != nil {
		return err
	}
	return writeJSON(stdout, identity)
}
