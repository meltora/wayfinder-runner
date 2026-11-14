package elevatedcapability

import (
	"net/url"
	"regexp"
	"strings"
)

var hostLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var topLevelLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func NormalizeHost(key string) (string, bool) {
	host := key
	wildcard := strings.HasPrefix(host, "*.")
	if wildcard {
		host = strings.TrimPrefix(host, "*.")
	}
	if host == "" || len(host) > 253 || host != strings.ToLower(host) {
		return "", false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 || !topLevelLabelPattern.MatchString(labels[len(labels)-1]) {
		return "", false
	}
	for _, label := range labels {
		if !hostLabelPattern.MatchString(label) {
			return "", false
		}
	}
	if wildcard {
		return "*." + host, true
	}
	return host, true
}

func HostAllowed(rawURL string, domains map[string]bool) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	if domains[host] {
		return true
	}
	labels := strings.Split(host, ".")
	for index := 1; index < len(labels)-1; index++ {
		if domains["*."+strings.Join(labels[index:], ".")] {
			return true
		}
	}
	return false
}
