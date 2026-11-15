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
		if wildcardForbiddenSuffixes[host] {
			return "", false
		}
		return "*." + host, true
	}
	return host, true
}

var wildcardForbiddenSuffixes = setOf(
	"co.uk", "org.uk", "ac.uk", "gov.uk", "me.uk", "ltd.uk", "plc.uk", "net.uk", "sch.uk",
	"com.au", "net.au", "org.au", "edu.au", "gov.au", "co.nz", "org.nz", "net.nz",
	"co.jp", "ne.jp", "or.jp", "ac.jp", "go.jp", "co.kr", "or.kr", "co.in", "net.in", "org.in",
	"co.za", "org.za", "com.br", "net.br", "org.br", "com.cn", "net.cn", "org.cn", "com.hk",
	"com.mx", "com.tr", "com.tw", "com.sg", "com.ar", "com.co", "co.id", "co.il", "com.pl",
	"github.io", "githubusercontent.com", "gitlab.io", "herokuapp.com", "vercel.app", "netlify.app",
	"pages.dev", "workers.dev", "appspot.com", "web.app", "firebaseapp.com", "cloudfront.net",
	"amazonaws.com", "azurewebsites.net", "blogspot.com", "fly.dev", "onrender.com", "ngrok.io",
	"ngrok-free.app", "trycloudflare.com", "glitch.me", "repl.co", "now.sh", "surge.sh",
)

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
