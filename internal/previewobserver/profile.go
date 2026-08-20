package previewobserver

import (
	"crypto/x509"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/safeio"
)

const (
	maxAuthorizationBytes = 8 << 10
	maxTrustedCABytes     = 1 << 20
)

type healthCheck struct {
	path             string
	expectedStatuses map[int]struct{}
	timeout          time.Duration
}

type profile struct {
	capability        v1.PreviewObservationProfileCapability
	hostnameSuffixes  []string
	privateCIDRs      []netip.Prefix
	trustedCAFile     string
	authorizationFile string
	rootCAs           *x509.CertPool
	authorization     string
	maxResponseBytes  int64
	healthChecks      map[string]healthCheck
}

func compileProfile(value config.PreviewObservationProfileConfig) (profile, error) {
	_, err := loadTrustedCAs(value.TrustedCAFile)
	if err != nil {
		return profile{}, err
	}
	_, err = loadAuthorization(value.AuthorizationFile)
	if err != nil {
		return profile{}, err
	}
	checkIDs := make([]string, 0, len(value.HealthChecks))
	checks := make(map[string]healthCheck, len(value.HealthChecks))
	for checkID, check := range value.HealthChecks {
		statuses := make(map[int]struct{}, len(check.ExpectedStatus))
		for _, status := range check.ExpectedStatus {
			statuses[status] = struct{}{}
		}
		checks[checkID] = healthCheck{
			path: check.Path, expectedStatuses: statuses,
			timeout: time.Duration(check.TimeoutSeconds) * time.Second,
		}
		checkIDs = append(checkIDs, checkID)
	}
	sort.Strings(checkIDs)
	privateCIDRs := make([]netip.Prefix, 0, len(value.AllowedPrivateCIDRs))
	for _, raw := range value.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return profile{}, fmt.Errorf("parse preview private CIDR: %w", err)
		}
		privateCIDRs = append(privateCIDRs, prefix)
	}
	return profile{
		capability: v1.PreviewObservationProfileCapability{
			BlueprintID: value.BlueprintID, Version: value.Version, Digest: value.Digest,
			HealthCheckIDs: checkIDs,
		},
		hostnameSuffixes:  append([]string(nil), value.AllowedHostnameSuffixes...),
		privateCIDRs:      privateCIDRs,
		trustedCAFile:     value.TrustedCAFile,
		authorizationFile: value.AuthorizationFile,
		maxResponseBytes:  int64(value.MaxResponseBytes),
		healthChecks:      checks,
	}, nil
}

func (compiled profile) withCurrentSecrets() (profile, error) {
	rootCAs, err := loadTrustedCAs(compiled.trustedCAFile)
	if err != nil {
		return profile{}, err
	}
	authorization, err := loadAuthorization(compiled.authorizationFile)
	if err != nil {
		return profile{}, err
	}
	compiled.rootCAs = rootCAs
	compiled.authorization = authorization
	return compiled, nil
}

func loadTrustedCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := safeio.ReadRegularFile(path, maxTrustedCABytes, 0o022)
	if err != nil {
		return nil, fmt.Errorf("read preview observation trust root: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("preview observation trust root contains no certificates")
	}
	return pool, nil
}

func loadAuthorization(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := safeio.ReadRegularFile(path, maxAuthorizationBytes, 0o077)
	if err != nil {
		return "", fmt.Errorf("read private preview observation credential: %w", err)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(value) < 8 || len(value) > maxAuthorizationBytes || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("preview observation credential has an invalid format")
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return "", fmt.Errorf("preview observation credential has an invalid format")
		}
	}
	return value, nil
}

func capabilityKey(blueprintID, version, digest string) string {
	return blueprintID + "\x00" + version + "\x00" + digest
}

func cloneCapability(value v1.PreviewObservationProfileCapability) v1.PreviewObservationProfileCapability {
	value.HealthCheckIDs = append([]string(nil), value.HealthCheckIDs...)
	return value
}
