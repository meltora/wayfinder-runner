package daemon

import (
	"strings"
	"testing"
)

func withRelease(t *testing.T, channel, url string) {
	t.Helper()
	previousChannel, previousURL := Channel, DefaultControlPlaneURL
	Channel, DefaultControlPlaneURL = channel, url
	t.Cleanup(func() {
		Channel, DefaultControlPlaneURL = previousChannel, previousURL
	})
}

func TestReleaseDefaultsSourceBuild(t *testing.T) {
	withRelease(t, "", "")
	release, err := ReleaseDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if release.Channel != SourceChannel || release.ControlPlaneURL != "" {
		t.Fatalf("source build = %+v", release)
	}
}

func TestReleaseDefaultsStampedChannels(t *testing.T) {
	for _, channel := range []string{"develop", "staging", "production"} {
		t.Run(channel, func(t *testing.T) {
			withRelease(t, channel, "https://control.example.test")
			release, err := ReleaseDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if release.Channel != channel || release.ControlPlaneURL != "https://control.example.test" {
				t.Fatalf("release = %+v", release)
			}
		})
	}
}

func TestReleaseDefaultsChannelWithoutURL(t *testing.T) {
	withRelease(t, "staging", "")
	release, err := ReleaseDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if release.Channel != "staging" || release.ControlPlaneURL != "" {
		t.Fatalf("release = %+v", release)
	}
}

func TestReleaseDefaultsNormalizesTrailingSlash(t *testing.T) {
	withRelease(t, "production", "https://Control.Example.test:8443/")
	release, err := ReleaseDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if release.ControlPlaneURL != "https://control.example.test:8443" {
		t.Fatalf("url = %q", release.ControlPlaneURL)
	}
}

func TestReleaseDefaultsRejectsInvalidStamps(t *testing.T) {
	cases := map[string][2]string{
		"unknown channel":     {"nightly", "https://control.example.test"},
		"url without channel": {"", "https://control.example.test"},
		"http":                {"develop", "http://control.example.test"},
		"path":                {"develop", "https://control.example.test/app"},
		"query":               {"develop", "https://control.example.test/?a=1"},
		"fragment":            {"develop", "https://control.example.test/#x"},
		"credentials":         {"develop", "https://user:secret@control.example.test"},
		"no host":             {"develop", "https://"},
		"whitespace":          {"develop", " https://control.example.test"},
		"relative":            {"develop", "control.example.test"},
	}
	for name, stamp := range cases {
		t.Run(name, func(t *testing.T) {
			withRelease(t, stamp[0], stamp[1])
			_, err := ReleaseDefaults()
			if err == nil {
				t.Fatalf("stamp %q %q accepted", stamp[0], stamp[1])
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks the stamped value: %v", err)
			}
		})
	}
}
