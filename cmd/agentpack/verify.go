package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/agentpack/trustset"
	"github.com/example/app-runner/internal/safeio"
)

const maxPublishRequestBytes = 16 << 10

type releaseVerification struct {
	Valid            bool   `json:"valid"`
	Digest           string `json:"digest"`
	Version          string `json:"version"`
	KeyID            string `json:"keyId"`
	TrustSetSequence int64  `json:"trustSetSequence"`
	RootKeyID        string `json:"rootKeyId"`
}

func runVerify(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("verify", stderr)
	rootsPath := flags.String("roots", "", "trust roots file (format 1)")
	setPath := flags.String("trust-set", "", "signed trusted key set")
	requestPath := flags.String("publish-request", "", "publish request written by agentpack sign")
	archivePath := flags.String("archive", "", "archive built by agentpack build")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"roots": *rootsPath, "trust-set": *setPath, "publish-request": *requestPath, "archive": *archivePath}); err != nil {
		return err
	}
	verified, err := verifySignedSetFile(*rootsPath, *setPath)
	if err != nil {
		return err
	}
	rawRequest, err := safeio.ReadRegularFile(*requestPath, maxPublishRequestBytes, 0)
	if err != nil {
		return refused("publish_request_unreadable", "the publish request must be a regular file of at most %d bytes", maxPublishRequestBytes)
	}
	var request agentpack.PublishRequest
	decoder := json.NewDecoder(bytes.NewReader(rawRequest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return refused("publish_request_invalid", "the publish request is not a strict agentpack sign document")
	}
	key, err := verified.TrustedKey(request.AgentPack.KeyID, time.Now())
	switch {
	case errors.Is(err, trustset.ErrKeyRevoked):
		return refused("signing_key_revoked", "signing key %s is revoked by trusted key set %d", request.AgentPack.KeyID, verified.Document.Sequence)
	case errors.Is(err, trustset.ErrKeyOutsideValidity):
		return refused("signing_key_outside_validity", "signing key %s is outside its validity window", request.AgentPack.KeyID)
	case err != nil:
		return refused("signing_key_untrusted", "signing key %s is not in trusted key set %d", request.AgentPack.KeyID, verified.Document.Sequence)
	}
	if err := agentpack.VerifyPublishRequest(request, key.PublicKeySPKIBase64); err != nil {
		return refused("attestation_invalid", "the publication attestation does not verify with signing key %s", key.KeyID)
	}
	archive, err := readArchiveFile(*archivePath)
	if err != nil {
		return refused("archive_invalid", "%v", err)
	}
	if archive.Digest != request.AgentPack.Digest || archive.Manifest.Version != request.AgentPack.Version ||
		archive.Manifest.PackKey != request.PackKey || archive.Manifest.Provider != request.Provider {
		return refused("archive_mismatch", "the archive does not match the signed publish request")
	}
	return writeJSON(stdout, releaseVerification{
		Valid: true, Digest: archive.Digest, Version: archive.Manifest.Version, KeyID: key.KeyID,
		TrustSetSequence: verified.Document.Sequence, RootKeyID: verified.RootKeyID,
	})
}
