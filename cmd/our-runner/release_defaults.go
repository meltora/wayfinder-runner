package main

import (
	"fmt"
	"io"

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
	if err != nil || release.ControlPlaneURL == "" {
		fmt.Fprintln(output, "control plane: none (enroll requires --url)")
		return
	}
	fmt.Fprintln(output, "control plane:", release.ControlPlaneURL)
}
