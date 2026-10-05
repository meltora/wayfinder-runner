package daemon

import (
	"errors"
	"net/url"
	"strings"
)

var (
	Channel                = ""
	DefaultControlPlaneURL = ""
)

const SourceChannel = "source"

type Release struct {
	Channel         string
	ControlPlaneURL string
}

var releaseChannels = map[string]bool{"develop": true, "staging": true, "production": true}

func ReleaseDefaults() (Release, error) {
	if Channel == "" {
		if DefaultControlPlaneURL != "" {
			return Release{}, errors.New("this build has a default control plane but no release channel")
		}
		return Release{Channel: SourceChannel}, nil
	}
	if !releaseChannels[Channel] {
		return Release{}, errors.New("this build has an unknown release channel")
	}
	if DefaultControlPlaneURL == "" {
		return Release{Channel: Channel}, nil
	}
	origin, ok := controlPlaneOrigin(DefaultControlPlaneURL)
	if !ok {
		return Release{}, errors.New("this build's default control plane is not an HTTPS origin")
	}
	return Release{Channel: Channel, ControlPlaneURL: origin}, nil
}

func controlPlaneOrigin(raw string) (string, bool) {
	if raw != strings.TrimSpace(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	return "https://" + strings.ToLower(u.Host), true
}
