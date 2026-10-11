package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/daemon"
)

const noDefaultControlPlane = "this build has no default control plane"

func defaultEnrollmentURL() (string, error) {
	release, err := daemon.ReleaseDefaults()
	if err != nil {
		return "", fmt.Errorf("enroll requires --url (%s)", err.Error())
	}
	if release.ControlPlaneURL == "" {
		return "", fmt.Errorf("enroll requires --url (%s)", noDefaultControlPlane)
	}
	return release.ControlPlaneURL, nil
}

func writeVersion(output io.Writer) {
	fmt.Fprintln(output, "our-runner", daemon.Version)
	release, err := daemon.ReleaseDefaults()
	switch {
	case err != nil:
		fmt.Fprintf(output, "channel: invalid (%s)\n", err.Error())
	default:
		fmt.Fprintln(output, "channel:", release.Channel)
	}
	fmt.Fprintln(output, "platform trust roots:", platformTrustRootsSummary())
	if err != nil || release.ControlPlaneURL == "" {
		fmt.Fprintln(output, "control plane: none (enroll requires --url)")
		return
	}
	fmt.Fprintln(output, "control plane:", release.ControlPlaneURL)
}

func platformTrustRootsSummary() string {
	roots, err := trustroot.Resolve(daemon.Channel, os.Getenv(trustroot.OverrideEnvironment))
	if err != nil {
		return "unavailable"
	}
	ids := roots.KeyIDs()
	if len(ids) == 0 {
		return "none"
	}
	summary := strings.Join(ids, ", ")
	if roots.Source == trustroot.SourceDevelopmentOverride {
		summary += " (development override)"
	}
	return summary
}
