package main

import (
	"io"

	"github.com/example/app-runner/internal/agentpack"
)

func runPublicKey(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("public-key", stderr)
	key := flags.String("key", "", "path of the private key file")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"key": *key}); err != nil {
		return err
	}
	signer, err := agentpack.NewFileSigner(*key)
	if err != nil {
		return err
	}
	identity, err := agentpack.SignerPublicKeyIdentity(signer)
	if err != nil {
		return err
	}
	return writeJSON(stdout, identity)
}
