package updatecatalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
	releasePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	formatPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.+-]{0,30}[a-z0-9])?$`)
)

const timestampLayout = "2006-01-02T15:04:05Z"

func validateManifest(manifest Manifest) error {
	if manifest.Schema != SchemaV2 {
		return invalid("unsupported schema %q", manifest.Schema)
	}
	if !namePattern.MatchString(manifest.Channel) {
		return invalid("invalid channel %q", manifest.Channel)
	}
	if manifest.Sequence == 0 {
		return invalid("sequence must be positive")
	}
	if !releasePattern.MatchString(manifest.ReleaseID) {
		return invalid("invalid release_id %q", manifest.ReleaseID)
	}
	version, err := parseSemanticVersion(manifest.Version)
	if err != nil {
		return invalid("%v", err)
	}
	if manifest.Channel == "stable" && len(version.prerelease) != 0 {
		return invalid("stable channel cannot contain a prerelease version")
	}
	if _, err := parseSemanticVersion(manifest.MinRunner); err != nil {
		return invalid("invalid min_runner: %v", err)
	}
	if _, err := parseSemanticVersion(manifest.MinUpdater); err != nil {
		return invalid("invalid min_updater: %v", err)
	}
	if err := validateTimestamp("issued_at", manifest.IssuedAt); err != nil {
		return err
	}
	if err := validateTimestamp("expires_at", manifest.ExpiresAt); err != nil {
		return err
	}
	if !manifest.ExpiresAt.After(manifest.IssuedAt) {
		return invalid("expires_at must be after issued_at")
	}
	if len(manifest.Artifacts) == 0 {
		return invalid("at least one artifact is required")
	}
	if len(manifest.Artifacts) > 64 {
		return invalid("too many artifacts")
	}
	platforms := make(map[string]struct{}, len(manifest.Artifacts))
	for i, artifact := range manifest.Artifacts {
		if err := validateArtifact(artifact, manifest); err != nil {
			return invalid("artifact %d: %v", i, err)
		}
		platform := artifact.OS + "/" + artifact.Arch
		if _, exists := platforms[platform]; exists {
			return invalid("duplicate platform %s", platform)
		}
		platforms[platform] = struct{}{}
	}
	return nil
}

func validateTimestamp(field string, value time.Time) error {
	if value.IsZero() || value.Nanosecond() != 0 {
		return invalid("%s must use whole-second UTC precision", field)
	}
	_, offset := value.Zone()
	if offset != 0 || value.Format(timestampLayout) != value.UTC().Format(timestampLayout) {
		return invalid("%s must be UTC", field)
	}
	return nil
}

func validateArtifact(artifact Artifact, manifest Manifest) error {
	if !namePattern.MatchString(artifact.OS) {
		return fmt.Errorf("invalid os %q", artifact.OS)
	}
	if !namePattern.MatchString(artifact.Arch) {
		return fmt.Errorf("invalid arch %q", artifact.Arch)
	}
	if !formatPattern.MatchString(artifact.Format) {
		return fmt.Errorf("invalid format %q", artifact.Format)
	}
	parsed, err := url.ParseRequestURI(artifact.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("url must be an immutable HTTPS URL without credentials, query, or fragment")
	}
	if parsed.String() != artifact.URL {
		return fmt.Errorf("url must use its canonical encoding")
	}
	if parsed.Host != strings.ToLower(parsed.Host) || parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("url host and path must be canonical and non-empty")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	immutable := false
	for _, segment := range segments {
		decoded, decodeErr := url.PathUnescape(segment)
		if decodeErr != nil {
			return fmt.Errorf("url contains invalid path escaping")
		}
		if decoded == "." || decoded == ".." || strings.ContainsAny(decoded, `/\\`) {
			return fmt.Errorf("url contains an ambiguous path segment")
		}
		lower := strings.ToLower(decoded)
		if lower == "latest" || lower == "current" || lower == "nightly" || lower == "snapshot" {
			return fmt.Errorf("url contains moving alias %q", decoded)
		}
		if decoded == manifest.Version || decoded == "v"+manifest.Version || decoded == manifest.ReleaseID || decoded == artifact.SHA256 {
			immutable = true
		}
	}
	if !immutable {
		return fmt.Errorf("url path must contain the version, release_id, or artifact digest")
	}
	if !digestPattern.MatchString(artifact.SHA256) {
		return fmt.Errorf("sha256 must be 64 lowercase hexadecimal characters")
	}
	if artifact.Size <= 0 || artifact.Size > MaxArtifactSize {
		return fmt.Errorf("size must be between 1 and %d", MaxArtifactSize)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

func SelectArtifact(manifest Manifest, goos, goarch string) (Artifact, error) {
	if !namePattern.MatchString(goos) || !namePattern.MatchString(goarch) {
		return Artifact{}, fmt.Errorf("%w: invalid requested platform", ErrNoArtifact)
	}
	var selected *Artifact
	for i := range manifest.Artifacts {
		artifact := manifest.Artifacts[i]
		if artifact.OS != goos || artifact.Arch != goarch {
			continue
		}
		if selected != nil {
			return Artifact{}, invalid("duplicate platform %s/%s", goos, goarch)
		}
		copy := artifact
		selected = &copy
	}
	if selected == nil {
		return Artifact{}, fmt.Errorf("%w: %s/%s", ErrNoArtifact, goos, goarch)
	}
	return *selected, nil
}
