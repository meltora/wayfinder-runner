package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/safeio"
)

type proveOutput struct {
	SignatureBase64 string `json:"signatureBase64"`
}

func runProve(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("prove", stderr)
	key := flags.String("key", "", "path of the private key file")
	messageFile := flags.String("message-file", "", "file holding the canonical AP-1 signing-key registration proof message")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"key": *key, "message-file": *messageFile}); err != nil {
		return err
	}
	message, err := safeio.ReadRegularFile(*messageFile, agentpack.SigningKeyProofMaxBytes, 0)
	if err != nil {
		return fmt.Errorf("message file %q must be a regular file of at most %d bytes", *messageFile, agentpack.SigningKeyProofMaxBytes)
	}
	signer, err := agentpack.NewFileSigner(*key)
	if err != nil {
		return err
	}
	signature, err := agentpack.SignSigningKeyProof(context.Background(), signer, message, time.Now())
	if err != nil {
		return err
	}
	return writeJSON(stdout, proveOutput{SignatureBase64: base64.StdEncoding.EncodeToString(signature)})
}
