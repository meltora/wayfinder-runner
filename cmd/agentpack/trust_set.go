package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/agentpack/trustset"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/strictjson"
)

const maxSignedSetBytes = 128 << 10

type trustSetSummary struct {
	RootKeyID      string `json:"rootKeyId"`
	Sequence       int64  `json:"sequence"`
	DocumentSHA256 string `json:"documentSha256"`
}

type trustSetVerification struct {
	Valid          bool     `json:"valid"`
	RootKeyID      string   `json:"rootKeyId"`
	Sequence       int64    `json:"sequence"`
	IssuedAt       string   `json:"issuedAt"`
	DocumentSHA256 string   `json:"documentSha256"`
	KeyIDs         []string `json:"keyIds"`
	RevokedKeyIDs  []string `json:"revokedKeyIds"`
}

func runTrustSet(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError{message: "expected trust-set sign or trust-set verify"}
	}
	switch args[0] {
	case "sign":
		return runTrustSetSign(args[1:], stdout, stderr)
	case "verify":
		return runTrustSetVerify(args[1:], stdout, stderr)
	default:
		return usageError{message: fmt.Sprintf("unknown trust-set command %q", args[0])}
	}
}

func runTrustSetSign(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("trust-set sign", stderr)
	rootKey := flags.String("root-key", "", "path of the offline root private key file")
	in := flags.String("in", "", "trusted key set JSON to canonicalize and sign")
	out := flags.String("out", "", "path of the signed set (never overwritten)")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"root-key": *rootKey, "in": *in, "out": *out}); err != nil {
		return err
	}
	raw, err := safeio.ReadRegularFile(*in, trustset.MaxDocumentBytes, 0)
	if err != nil {
		return refused("input_unreadable", "the set file must be a regular file of at most %d bytes", trustset.MaxDocumentBytes)
	}
	document, err := trustset.ParseInput(raw)
	if err != nil {
		return trustSetRefusal(err)
	}
	signer, err := agentpack.NewFileSigner(*rootKey)
	if err != nil {
		return refused("root_key_unreadable", "%v", err)
	}
	signed, err := trustset.Sign(context.Background(), signer, document)
	if err != nil {
		return trustSetRefusal(err)
	}
	encoded, err := json.Marshal(signed)
	if err != nil {
		return err
	}
	if err := writeNewFile(*out, append(encoded, '\n')); err != nil {
		return err
	}
	sum := sha256.Sum256(document.Canonical())
	return writeJSON(stdout, trustSetSummary{RootKeyID: signed.RootKeyID, Sequence: document.Sequence, DocumentSHA256: hex.EncodeToString(sum[:])})
}

func runTrustSetVerify(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("trust-set verify", stderr)
	rootsPath := flags.String("roots", "", "trust roots file (format 1)")
	in := flags.String("in", "", "signed trusted key set")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"roots": *rootsPath, "in": *in}); err != nil {
		return err
	}
	verified, err := verifySignedSetFile(*rootsPath, *in)
	if err != nil {
		return err
	}
	keyIDs := make([]string, 0, len(verified.Document.Keys))
	for _, key := range verified.Document.Keys {
		keyIDs = append(keyIDs, key.KeyID)
	}
	return writeJSON(stdout, trustSetVerification{
		Valid: true, RootKeyID: verified.RootKeyID, Sequence: verified.Document.Sequence,
		IssuedAt: verified.Document.IssuedAt.UTC().Format("2006-01-02T15:04:05Z"), DocumentSHA256: verified.DocumentSHA256,
		KeyIDs: keyIDs, RevokedKeyIDs: append([]string{}, verified.Document.RevokedKeyIDs...),
	})
}

func verifySignedSetFile(rootsPath, setPath string) (trustset.Verified, error) {
	rawRoots, err := safeio.ReadRegularFile(rootsPath, trustroot.MaxRootsFileBytes, 0)
	if err != nil {
		return trustset.Verified{}, refused("roots_unreadable", "the roots file must be a regular file of at most %d bytes", trustroot.MaxRootsFileBytes)
	}
	roots, err := trustroot.ParseRootsFile(rawRoots)
	if err != nil {
		return trustset.Verified{}, refused("roots_invalid", "%v", err)
	}
	rawSet, err := safeio.ReadRegularFile(setPath, maxSignedSetBytes, 0)
	if err != nil {
		return trustset.Verified{}, refused("input_unreadable", "the signed set must be a regular file of at most %d bytes", maxSignedSetBytes)
	}
	signed, err := decodeSignedSet(rawSet)
	if err != nil {
		return trustset.Verified{}, err
	}
	verified, err := trustset.Verify(signed, trustroot.Roots{Channel: "configured", Keys: roots})
	if err != nil {
		return trustset.Verified{}, trustSetRefusal(err)
	}
	return verified, nil
}

func decodeSignedSet(raw []byte) (v1.AgentPackTrustedKeySet, error) {
	var signed v1.AgentPackTrustedKeySet
	fields, err := strictjson.Object(raw)
	if err != nil {
		return signed, refused(trustset.CodeInvalidEnvelope, "%v", err)
	}
	names := []string{"document_base64", "root_key_id", "signature_base64"}
	if err := strictjson.ExactFields(fields, names, names); err != nil {
		return signed, refused(trustset.CodeInvalidEnvelope, "%v", err)
	}
	for name, target := range map[string]*string{"document_base64": &signed.DocumentBase64, "root_key_id": &signed.RootKeyID, "signature_base64": &signed.SignatureBase64} {
		value, err := strictjson.String(fields[name])
		if err != nil {
			return signed, refused(trustset.CodeInvalidEnvelope, "%s must be a string", name)
		}
		*target = value
	}
	return signed, nil
}

func trustSetRefusal(err error) error {
	var failure *trustset.Error
	if errors.As(err, &failure) {
		return refused(failure.Code, "%s", failure.Detail)
	}
	return refused("signing_failed", "%v", err)
}

func writeNewFile(path string, contents []byte) error {
	// #nosec G302 -- the signed trusted key set is public integrity-protected material that services running as other users must read
	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return refused("output_exists", "%s already exists and is never overwritten", path)
	}
	if err != nil {
		return refused("output_unwritable", "%s cannot be created", path)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return refused("output_unwritable", "%s could not be written", path)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return refused("output_unwritable", "%s could not be written", path)
	}
	return file.Close()
}
