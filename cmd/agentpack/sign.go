package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strconv"

	"github.com/example/app-runner/internal/agentpack"
)

func runSign(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("sign", stderr)
	key := flags.String("key", "", "path of the private key file")
	archivePath := flags.String("archive", "", "archive built by agentpack build")
	scope := flags.String("scope", "", "platform or organization")
	organizationID := flags.String("organization-id", "", "organization id for organization scope")
	packKey := flags.String("pack-key", "", "pack key; must equal the archive manifest")
	packName := flags.String("pack-name", "", "display name")
	provider := flags.String("provider", "", "provider; must equal the archive manifest")
	version := flags.String("version", "", "optional; must equal the archive manifest version")
	protocolVersion := flags.String("protocol-version", "", "runner protocol version")
	minimumRunnerVersion := flags.String("minimum-runner-version", "", "minimum runner version")
	keyID := flags.String("key-id", "", "registered signing key id")
	gatewayOrigin := flags.String("gateway-origin", "", "https origin of the runner gateway")
	explicitURL := flags.String("url", "", "explicit https archive URL")
	requestID := flags.String("request-id", "", "publish request id (default: random UUID v4)")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{
		"key": *key, "archive": *archivePath, "scope": *scope, "pack-key": *packKey, "pack-name": *packName,
		"provider": *provider, "protocol-version": *protocolVersion, "minimum-runner-version": *minimumRunnerVersion, "key-id": *keyID,
	}); err != nil {
		return err
	}
	if (*gatewayOrigin == "") == (*explicitURL == "") {
		return usageError{message: "exactly one of --gateway-origin or --url is required"}
	}
	if *scope != "platform" && *scope != "organization" {
		return usageError{message: "--scope must be platform or organization"}
	}
	protocol, err := strconv.Atoi(*protocolVersion)
	if err != nil {
		return usageError{message: "--protocol-version must be an integer"}
	}
	archive, err := readArchiveFile(*archivePath)
	if err != nil {
		return err
	}
	if *version != "" && *version != archive.Manifest.Version {
		return fmt.Errorf("version %q does not match the archive manifest version %q", *version, archive.Manifest.Version)
	}
	location := *explicitURL
	if *gatewayOrigin != "" {
		location, err = agentpack.GatewayArchiveURL(*gatewayOrigin, archive.Digest)
		if err != nil {
			return err
		}
	}
	if *requestID == "" {
		*requestID, err = randomUUIDv4()
		if err != nil {
			return err
		}
	}
	signer, err := agentpack.NewFileSigner(*key)
	if err != nil {
		return err
	}
	request, err := agentpack.SignPublishRequest(context.Background(), signer, agentpack.PublicationRequest{
		ScopeKind: *scope, OrganizationID: *organizationID, PackKey: *packKey, PackName: *packName, Provider: *provider,
		ProtocolVersion: protocol, MinimumRunnerVersion: *minimumRunnerVersion, KeyID: *keyID, URL: location,
		RequestID: *requestID, Archive: archive,
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, request)
}

func randomUUIDv4() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("request id generation failed")
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
