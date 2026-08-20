package previewobserver

import (
	"context"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type resolvedTarget struct {
	hostname string
	address  netip.Addr
}

const (
	maxResolvedAddresses = 32
	resolverTimeout      = 10 * time.Second
)

func (registry *Registry) resolveTarget(
	ctx context.Context,
	compiled profile,
	rawURL string,
) (resolvedTarget, error) {
	hostname, err := allowedHostname(compiled, rawURL)
	if err != nil {
		return resolvedTarget{}, err
	}
	resolverCtx, cancel := context.WithTimeout(ctx, resolverTimeout)
	defer cancel()
	addresses, err := registry.dependencies.LookupNetIP(resolverCtx, "ip", hostname)
	if err != nil || len(addresses) == 0 {
		return resolvedTarget{}, resultError(v1.PreviewUnreachable)
	}
	if len(addresses) > maxResolvedAddresses {
		return resolvedTarget{}, resultError(v1.PreviewInvalidObserverResponse)
	}
	normalized := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !addressAllowed(compiled, address) {
			return resolvedTarget{}, resultError(v1.PreviewTargetNotAllowed)
		}
		if _, exists := seen[address]; !exists {
			seen[address] = struct{}{}
			normalized = append(normalized, address)
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].Compare(normalized[right]) < 0
	})
	return resolvedTarget{hostname: hostname, address: normalized[0]}, nil
}

func allowedHostname(compiled profile, rawURL string) (string, error) {
	if rawURL == "" || len(rawURL) > 4096 || strings.ContainsAny(rawURL, "\\\x00\r\n\t ") {
		return "", resultError(v1.PreviewTargetNotAllowed)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Port() != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.RawPath != "" || parsed.Host != strings.ToLower(parsed.Host) {
		return "", resultError(v1.PreviewTargetNotAllowed)
	}
	hostname := parsed.Hostname()
	if !canonicalTargetDomain(hostname) || !strings.Contains(hostname, ".") ||
		strings.HasSuffix(hostname, ".") || hostname == "localhost" {
		return "", resultError(v1.PreviewTargetNotAllowed)
	}
	if _, err := netip.ParseAddr(hostname); err == nil {
		return "", resultError(v1.PreviewTargetNotAllowed)
	}
	for _, suffix := range compiled.hostnameSuffixes {
		if hostname == suffix || strings.HasSuffix(hostname, "."+suffix) {
			return hostname, nil
		}
	}
	return "", resultError(v1.PreviewTargetNotAllowed)
}

func canonicalTargetDomain(hostname string) bool {
	if !canonicalTargetHostname.MatchString(hostname) || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func addressAllowed(compiled profile, address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	if address.IsPrivate() {
		for _, prefix := range compiled.privateCIDRs {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	if !address.IsGlobalUnicast() || addressInDeniedSpecialRange(address) {
		return false
	}
	return true
}

func addressInDeniedSpecialRange(address netip.Addr) bool {
	for _, prefix := range deniedSpecialRanges {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var canonicalTargetHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

var deniedSpecialRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}
