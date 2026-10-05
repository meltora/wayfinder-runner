package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/remoteconfig"
	"github.com/example/app-runner/internal/safeio"
)

const gatewayURLFileName = "gateway-url"

const gatewayNotProvidedMessage = "The gateway address was not provided; pass --gateway URL to our-runner configure."

var errNoKnownGateway = errors.New("No gateway address is known for this runner; pass --gateway URL.")

func discoverGatewayURL(client *enroll.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gatewayURL, err := client.DiscoverGateway(ctx)
	if err != nil {
		return "", err
	}
	if _, err := remoteconfig.ConfigurationURL(gatewayURL); err != nil {
		return "", fmt.Errorf("discovered gateway: %w", err)
	}
	return gatewayURL, nil
}

func recordDiscoveredGateway(client *enroll.Client, stateDir string) {
	gatewayURL, err := discoverGatewayURL(client)
	if err == nil {
		err = safeio.AtomicWriteFile(stateDir, gatewayURLFileName, []byte(gatewayURL+"\n"), 0o600)
	}
	if err != nil {
		fmt.Println(gatewayNotProvidedMessage)
	}
}

func loadStoredGatewayURL(stateDir string) (string, bool, error) {
	raw, err := safeio.ReadRegularFile(filepath.Join(stateDir, gatewayURLFileName), 4096, 0o077)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", gatewayURLFileName, err)
	}
	gatewayURL := strings.TrimSpace(string(raw))
	if _, err := remoteconfig.ConfigurationURL(gatewayURL); err != nil {
		return "", false, fmt.Errorf("%s: %w", gatewayURLFileName, err)
	}
	return gatewayURL, true, nil
}

func gatewayArgument(gatewayURL string, fromStoredFile bool) string {
	if fromStoredFile {
		return ""
	}
	return " --gateway " + shellQuote(gatewayURL)
}
