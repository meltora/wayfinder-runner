package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxPreviewObservationProfiles          = 128
	maxPreviewObservationHostnameSuffixes  = 32
	maxPreviewObservationPrivateCIDRs      = 32
	maxPreviewObservationHealthChecks      = 128
	defaultPreviewObservationResponseBytes = 64 << 10
	maxPreviewObservationResponseBytes     = 1 << 20
	defaultPreviewObservationTimeout       = 10
	maxPreviewObservationTimeout           = 120
)

type PreviewObservationProfileConfig struct {
	BlueprintID             string                                         `yaml:"blueprint_id"`
	Version                 string                                         `yaml:"version"`
	Digest                  string                                         `yaml:"digest"`
	AllowedHostnameSuffixes []string                                       `yaml:"allowed_hostname_suffixes"`
	AllowedPrivateCIDRs     []string                                       `yaml:"allowed_private_cidrs,omitempty"`
	TrustedCAFile           string                                         `yaml:"trusted_ca_file,omitempty"`
	AuthorizationFile       string                                         `yaml:"authorization_file,omitempty"`
	MaxResponseBytes        int                                            `yaml:"max_response_bytes,omitempty"`
	HealthChecks            map[string]PreviewObservationHealthCheckConfig `yaml:"health_checks"`
}

type PreviewObservationHealthCheckConfig struct {
	Path           string `yaml:"path"`
	ExpectedStatus []int  `yaml:"expected_status,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds,omitempty"`
}

func normalizePreviewObservationProfiles(
	profiles map[string]PreviewObservationProfileConfig,
) map[string]PreviewObservationProfileConfig {
	if profiles == nil {
		return nil
	}
	result := make(map[string]PreviewObservationProfileConfig, len(profiles))
	for id, profile := range profiles {
		copyProfile := profile
		copyProfile.AllowedHostnameSuffixes = append([]string(nil), profile.AllowedHostnameSuffixes...)
		copyProfile.AllowedPrivateCIDRs = append([]string(nil), profile.AllowedPrivateCIDRs...)
		if copyProfile.MaxResponseBytes == 0 {
			copyProfile.MaxResponseBytes = defaultPreviewObservationResponseBytes
		}
		copyProfile.HealthChecks = make(map[string]PreviewObservationHealthCheckConfig, len(profile.HealthChecks))
		for checkID, check := range profile.HealthChecks {
			copyCheck := check
			copyCheck.ExpectedStatus = append([]int(nil), check.ExpectedStatus...)
			if len(copyCheck.ExpectedStatus) == 0 {
				copyCheck.ExpectedStatus = []int{200}
			}
			if copyCheck.TimeoutSeconds == 0 {
				copyCheck.TimeoutSeconds = defaultPreviewObservationTimeout
			}
			copyProfile.HealthChecks[checkID] = copyCheck
		}
		result[id] = copyProfile
	}
	return result
}

func validatePreviewObservationProfiles(profiles map[string]PreviewObservationProfileConfig) error {
	if len(profiles) > MaxPreviewObservationProfiles {
		return fmt.Errorf("preview_observation_profiles must contain at most %d entries", MaxPreviewObservationProfiles)
	}
	for id, profile := range profiles {
		if !validConfigID(id) {
			return fmt.Errorf("preview observation profile ID %q is invalid", id)
		}
		if err := validatePreviewObservationProfile(id, profile); err != nil {
			return err
		}
	}
	return nil
}

func validatePreviewObservationProfile(id string, profile PreviewObservationProfileConfig) error {
	if !validConfigID(profile.BlueprintID) {
		return fmt.Errorf("preview observation profile %s blueprint_id is invalid", id)
	}
	if strings.TrimSpace(profile.Version) == "" || len(profile.Version) > 128 ||
		strings.ContainsAny(profile.Version, "\x00\r\n") {
		return fmt.Errorf("preview observation profile %s version is invalid", id)
	}
	if !lowerSHA256Pattern.MatchString(profile.Digest) {
		return fmt.Errorf("preview observation profile %s digest is invalid", id)
	}
	if err := validatePreviewHostnameSuffixes(profile.AllowedHostnameSuffixes); err != nil {
		return fmt.Errorf("preview observation profile %s: %w", id, err)
	}
	if err := validatePreviewPrivateCIDRs(profile.AllowedPrivateCIDRs); err != nil {
		return fmt.Errorf("preview observation profile %s: %w", id, err)
	}
	for field, value := range map[string]string{
		"trusted_ca_file":    profile.TrustedCAFile,
		"authorization_file": profile.AuthorizationFile,
	} {
		if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00')) {
			return fmt.Errorf("preview observation profile %s %s must be a clean absolute path", id, field)
		}
	}
	if profile.MaxResponseBytes < 1 || profile.MaxResponseBytes > maxPreviewObservationResponseBytes {
		return fmt.Errorf("preview observation profile %s max_response_bytes is invalid", id)
	}
	if len(profile.HealthChecks) < 1 || len(profile.HealthChecks) > maxPreviewObservationHealthChecks {
		return fmt.Errorf("preview observation profile %s health_checks is invalid", id)
	}
	for checkID, check := range profile.HealthChecks {
		if !validConfigID(checkID) {
			return fmt.Errorf("preview observation profile %s health check ID %q is invalid", id, checkID)
		}
		if err := validatePreviewHealthCheck(check); err != nil {
			return fmt.Errorf("preview observation profile %s health check %s: %w", id, checkID, err)
		}
	}
	return nil
}

func validatePreviewHostnameSuffixes(values []string) error {
	if len(values) < 1 || len(values) > maxPreviewObservationHostnameSuffixes {
		return fmt.Errorf("allowed_hostname_suffixes is invalid")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalHostname(value) || !strings.Contains(value, ".") ||
			len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
			return fmt.Errorf("allowed hostname suffix %q is invalid", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("allowed hostname suffix %q is duplicated", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func canonicalHostname(value string) bool {
	if !canonicalHostnamePattern.MatchString(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func validatePreviewPrivateCIDRs(values []string) error {
	if len(values) > maxPreviewObservationPrivateCIDRs {
		return fmt.Errorf("allowed_private_cidrs is invalid")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Masked().String() != value || !privatePrefixAllowed(prefix) {
			return fmt.Errorf("allowed private CIDR %q is invalid", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("allowed private CIDR %q is duplicated", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func privatePrefixAllowed(prefix netip.Prefix) bool {
	allowed := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
	for _, private := range allowed {
		if prefix.Addr().BitLen() == private.Addr().BitLen() && prefix.Bits() >= private.Bits() &&
			private.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func validatePreviewHealthCheck(check PreviewObservationHealthCheckConfig) error {
	if len(check.Path) < 1 || len(check.Path) > 2048 || !strings.HasPrefix(check.Path, "/") ||
		strings.ContainsAny(check.Path, "\\%?#\x00\r\n\t ") || path.Clean(check.Path) != check.Path {
		return fmt.Errorf("path is invalid")
	}
	parsed, err := url.ParseRequestURI(check.Path)
	if err != nil || parsed.IsAbs() || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("path is invalid")
	}
	if len(check.ExpectedStatus) < 1 || len(check.ExpectedStatus) > 32 || !sort.IntsAreSorted(check.ExpectedStatus) {
		return fmt.Errorf("expected_status is invalid")
	}
	previous := 0
	for _, status := range check.ExpectedStatus {
		if status < 100 || status > 599 || status == previous {
			return fmt.Errorf("expected_status is invalid")
		}
		previous = status
	}
	if check.TimeoutSeconds < 1 || check.TimeoutSeconds > maxPreviewObservationTimeout {
		return fmt.Errorf("timeout_seconds is invalid")
	}
	return nil
}

func validConfigID(value string) bool {
	return len(value) <= 128 && value != "." && value != ".." && idPattern.MatchString(value)
}

var lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var canonicalHostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
