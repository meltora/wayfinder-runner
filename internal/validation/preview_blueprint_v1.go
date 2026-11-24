package validation

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	previewBlueprintMaxCommandBytes = 2048
	previewBlueprintCredentialText  = "This text looks like it contains a live credential; declare it as a secret instead"
	previewBlueprintECMAWhitespace  = `\t\n\x{0b}\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
)

var (
	previewBlueprintControlCharacter = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	previewBlueprintPinnedImage      = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)
	previewBlueprintServiceName      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	previewBlueprintEnvKey           = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	previewBlueprintHealthCheckID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	previewBlueprintHostname         = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	previewBlueprintLiveCredentials  = []*regexp.Regexp{
		regexp.MustCompile(`sk_live_`),
		regexp.MustCompile(`rk_live_`),
		regexp.MustCompile(`AKIA[0-9A-Z]{12,}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_`),
		regexp.MustCompile(`glpat-`),
		regexp.MustCompile(`whsec_`),
		regexp.MustCompile(`xox[abeprs]-`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^` + previewBlueprintECMAWhitespace + `/?#@:]*:[^` + previewBlueprintECMAWhitespace + `/?#@]+@`),
		regexp.MustCompile(`(?i)[?&;](?:password|passwd|pwd|pass)=[^` + previewBlueprintECMAWhitespace + `&#;$][^` + previewBlueprintECMAWhitespace + `&#;]*`),
		regexp.MustCompile(`(?i)(?:password|passwd)=['"]?[^` + previewBlueprintECMAWhitespace + `'"$][^` + previewBlueprintECMAWhitespace + `'"]*`),
	}
	previewBlueprintTokenSeparator       = regexp.MustCompile(`[^A-Za-z0-9+/=_-]+`)
	previewBlueprintInternalHost         = regexp.MustCompile(`(?:^|\.)(?:localhost|local|localdomain|internal|intranet|lan|home\.arpa|svc|corp|test|invalid|onion|example|nip\.io|sslip\.io|localtest\.me|lvh\.me)$`)
	previewBlueprintEncodedSeparator     = regexp.MustCompile(`(?i)%(?:2e|2f|5c)`)
	previewBlueprintHealthPathForbidden  = regexp.MustCompile(`[?#\\` + previewBlueprintECMAWhitespace + `]`)
	previewBlueprintGeneratorRequiredFor = map[string]string{
		"postgres_url": "database",
		"redis_url":    "cache",
		"smtp_url":     "mail_sink",
	}
)

func ParsePreviewBlueprintContentV1(raw []byte) (v1.PreviewBlueprintContentV1, error) {
	content, err := v1.ParsePreviewBlueprintContentV1(raw)
	if err != nil {
		return v1.PreviewBlueprintContentV1{}, err
	}
	if err := PreviewBlueprintContentV1(content); err != nil {
		return v1.PreviewBlueprintContentV1{}, err
	}
	return content, nil
}

func PreviewBlueprintContentV1(content v1.PreviewBlueprintContentV1) error {
	checker := &previewBlueprintChecker{}
	checker.content(content)
	if len(checker.issues) > 0 {
		return &v1.PreviewBlueprintContentErrorV1{Issues: checker.issues}
	}
	return nil
}

func LooksLikePreviewBlueprintLiveCredential(value string) bool {
	for _, pattern := range previewBlueprintLiveCredentials {
		if pattern.MatchString(value) {
			return true
		}
	}
	for _, token := range previewBlueprintTokenSeparator.Split(value, -1) {
		if len(token) >= 32 &&
			strings.ContainsAny(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
			strings.ContainsAny(token, "abcdefghijklmnopqrstuvwxyz") &&
			strings.ContainsAny(token, "0123456789") &&
			previewBlueprintShannonEntropy(token) >= 4 {
			return true
		}
	}
	return false
}

func previewBlueprintShannonEntropy(value string) float64 {
	var order []rune
	counts := map[rune]int{}
	for _, character := range value {
		if counts[character] == 0 {
			order = append(order, character)
		}
		counts[character]++
	}
	length := float64(len(value))
	entropy := 0.0
	for _, character := range order {
		probability := float64(counts[character]) / length
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}

func previewBlueprintTextLength(value string) int {
	return len(v1.PreviewBlueprintTextCodePoints(value))
}

type previewBlueprintChecker struct {
	issues         []v1.PreviewBlueprintIssueV1
	nonContinuable int
}

func (checker *previewBlueprintChecker) add(path []string, message string) {
	checker.issues = append(checker.issues, v1.PreviewBlueprintIssueV1{Path: slices.Clone(path), Message: message})
}

func (checker *previewBlueprintChecker) abort(path []string, message string) {
	checker.add(path, message)
	checker.nonContinuable++
}

func previewBlueprintPath(path []string, segments ...string) []string {
	return append(slices.Clone(path), segments...)
}

func (checker *previewBlueprintChecker) content(content v1.PreviewBlueprintContentV1) {
	start := checker.nonContinuable
	if content.SchemaVersion != v1.PreviewBlueprintContentSchemaVersionV1 {
		checker.abort([]string{"schema_version"}, "Invalid input: expected 1")
	}
	if !checker.overCap([]string{"services"}, len(content.Services), 8) {
		for index, service := range content.Services {
			checker.service(previewBlueprintPath(nil, "services", strconv.Itoa(index)), service)
		}
	}
	checker.app([]string{"app"}, content.App)
	checker.limits([]string{"limits"}, content.Limits)
	checker.egress([]string{"egress"}, content.Egress)
	if checker.nonContinuable != start {
		return
	}
	seenNames := map[string]bool{}
	roles := map[string]bool{}
	for index, service := range content.Services {
		if seenNames[service.Name] {
			checker.add(previewBlueprintPath(nil, "services", strconv.Itoa(index), "name"), "Service names must be unique")
		}
		seenNames[service.Name] = true
		roles[service.Role] = true
	}
	seenChecks := map[string]bool{}
	for index, check := range content.App.HealthChecks {
		if seenChecks[check.ID] {
			checker.add(previewBlueprintPath(nil, "app", "health_checks", strconv.Itoa(index), "id"), "Health check ids must be unique")
		}
		seenChecks[check.ID] = true
	}
	checker.generators(content.App.Env, roles, []string{"app", "env"})
	for index, service := range content.Services {
		checker.generators(service.Env, roles, previewBlueprintPath(nil, "services", strconv.Itoa(index), "env"))
	}
}

func (checker *previewBlueprintChecker) generators(env map[string]v1.PreviewBlueprintEnvEntryV1, roles map[string]bool, path []string) {
	for _, key := range sortedPreviewBlueprintKeys(env) {
		entry := env[key]
		if entry.Kind != v1.PreviewBlueprintEnvGenerated {
			continue
		}
		role, ok := previewBlueprintGeneratorRequiredFor[entry.Generator]
		if ok && !roles[role] {
			checker.add(previewBlueprintPath(path, key, "generator"), fmt.Sprintf("%s needs a service with the %s role", entry.Generator, role))
		}
	}
}

func sortedPreviewBlueprintKeys(env map[string]v1.PreviewBlueprintEnvEntryV1) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (checker *previewBlueprintChecker) service(path []string, service v1.PreviewBlueprintServiceV1) {
	if !previewBlueprintServiceName.MatchString(service.Name) {
		checker.add(previewBlueprintPath(path, "name"), "Invalid service name")
	}
	switch service.Role {
	case "database", "cache", "mail_sink", "other":
	default:
		checker.abort(previewBlueprintPath(path, "role"), "Invalid option: expected database, cache, mail_sink or other")
	}
	checker.pinnedImage(previewBlueprintPath(path, "image"), service.Image)
	if service.Env != nil {
		checker.env(previewBlueprintPath(path, "env"), service.Env)
	}
	if service.Health != nil {
		healthPath := previewBlueprintPath(path, "health")
		commandPath := previewBlueprintPath(healthPath, "command")
		if !checker.overCap(commandPath, len(service.Health.Command), 16) {
			for index, command := range service.Health.Command {
				checker.plainText(previewBlueprintPath(commandPath, strconv.Itoa(index)), command, 256)
			}
			checker.atLeast(commandPath, len(service.Health.Command), 1)
		}
		checker.integerRange(previewBlueprintPath(healthPath, "interval_seconds"), service.Health.IntervalSeconds, 1, 300)
	}
}

func (checker *previewBlueprintChecker) app(path []string, app v1.PreviewBlueprintAppV1) {
	checker.pinnedImage(previewBlueprintPath(path, "image"), app.Image)
	directoryPath := previewBlueprintPath(path, "working_directory")
	checker.plainText(directoryPath, app.WorkingDirectory, 256)
	if strings.HasPrefix(app.WorkingDirectory, "/") ||
		strings.Contains(app.WorkingDirectory, `\`) ||
		slices.Contains(strings.Split(app.WorkingDirectory, "/"), "..") {
		checker.add(directoryPath, "Working directory must be relative and stay inside the repository")
	}
	for _, phase := range []struct {
		name     string
		commands []string
	}{{"install", app.Install}, {"build", app.Build}, {"migrate", app.Migrate}, {"seed", app.Seed}} {
		phasePath := previewBlueprintPath(path, phase.name)
		if checker.overCap(phasePath, len(phase.commands), 8) {
			continue
		}
		for index, command := range phase.commands {
			checker.command(previewBlueprintPath(phasePath, strconv.Itoa(index)), command)
		}
	}
	checker.command(previewBlueprintPath(path, "start"), app.Start)
	checker.integerRange(previewBlueprintPath(path, "port"), app.Port, 1024, 65535)
	checksPath := previewBlueprintPath(path, "health_checks")
	if !checker.overCap(checksPath, len(app.HealthChecks), 16) {
		for index, check := range app.HealthChecks {
			checkPath := previewBlueprintPath(checksPath, strconv.Itoa(index))
			if !previewBlueprintHealthCheckID.MatchString(check.ID) {
				checker.add(previewBlueprintPath(checkPath, "id"), "Invalid health check id")
			}
			checker.healthPath(previewBlueprintPath(checkPath, "path"), check.Path)
			checker.integerRange(previewBlueprintPath(checkPath, "expected_status"), check.ExpectedStatus, 100, 599)
		}
		checker.atLeast(checksPath, len(app.HealthChecks), 1)
	}
	checker.env(previewBlueprintPath(path, "env"), app.Env)
}

func (checker *previewBlueprintChecker) limits(path []string, limits v1.PreviewBlueprintLimitsV1) {
	checker.integerRange(previewBlueprintPath(path, "memory_mb"), limits.MemoryMB, 128, 8192)
	cpusPath := previewBlueprintPath(path, "cpus")
	if limits.CPUs < 0.25 {
		checker.add(cpusPath, "Too small: expected at least 0.25")
	}
	if limits.CPUs > 4 {
		checker.add(cpusPath, "Too big: expected at most 4")
	}
	if quarters := limits.CPUs * 4; math.Trunc(quarters) != quarters || math.IsInf(quarters, 0) {
		checker.add(cpusPath, "CPUs must be a multiple of 0.25")
	}
	checker.integerRange(previewBlueprintPath(path, "max_lifetime_minutes"), limits.MaxLifetimeMinutes, 5, 43200)
}

func (checker *previewBlueprintChecker) egress(path []string, egress v1.PreviewBlueprintEgressV1) {
	if egress.Mode != v1.PreviewBlueprintEgressAllow {
		return
	}
	hostsPath := previewBlueprintPath(path, "hosts")
	if checker.overCap(hostsPath, len(egress.Hosts), 32) {
		return
	}
	for index, host := range egress.Hosts {
		if !previewBlueprintIsPublicHost(host) {
			checker.add(previewBlueprintPath(hostsPath, strconv.Itoa(index)), "Egress host must be a public DNS name, not localhost, an IP address or an internal suffix")
		}
	}
	checker.atLeast(hostsPath, len(egress.Hosts), 1)
	for index, host := range egress.Hosts {
		if slices.Index(egress.Hosts, host) != index {
			checker.add(previewBlueprintPath(hostsPath, strconv.Itoa(index)), "Egress hosts must be unique")
		}
	}
}

func (checker *previewBlueprintChecker) env(path []string, env map[string]v1.PreviewBlueprintEnvEntryV1) {
	start := checker.nonContinuable
	for _, key := range sortedPreviewBlueprintKeys(env) {
		entry := env[key]
		entryPath := previewBlueprintPath(path, key)
		if !previewBlueprintEnvKey.MatchString(key) {
			checker.abort(entryPath, "Invalid environment key")
			continue
		}
		switch entry.Kind {
		case v1.PreviewBlueprintEnvGenerated:
			switch entry.Generator {
			case "postgres_url", "redis_url", "smtp_url", "preview_url", "random_secret":
			default:
				checker.abort(previewBlueprintPath(entryPath, "generator"), "Invalid option: expected postgres_url, redis_url, smtp_url, preview_url or random_secret")
			}
		case v1.PreviewBlueprintEnvFake:
			checker.checkedText(previewBlueprintPath(entryPath, "value"), entry.Value, 0, 1024)
		case v1.PreviewBlueprintEnvSecret, v1.PreviewBlueprintEnvDisabled:
			if entry.Note != nil {
				checker.plainText(previewBlueprintPath(entryPath, "note"), *entry.Note, 280)
			}
		default:
			checker.abort(previewBlueprintPath(entryPath, "kind"), "Invalid env kind")
		}
	}
	if checker.nonContinuable == start && len(env) > 128 {
		checker.add(path, "At most 128 environment keys are allowed")
	}
}

func (checker *previewBlueprintChecker) overCap(path []string, length, maximum int) bool {
	if length <= maximum {
		return false
	}
	checker.abort(path, fmt.Sprintf("Too big: expected at most %d items", maximum))
	return true
}

func (checker *previewBlueprintChecker) atLeast(path []string, length, minimum int) {
	if length < minimum {
		checker.add(path, fmt.Sprintf("Too small: expected at least %d items", minimum))
	}
}

func (checker *previewBlueprintChecker) integerRange(path []string, value, minimum, maximum int) {
	if value < minimum {
		checker.add(path, fmt.Sprintf("Too small: expected at least %d", minimum))
	}
	if value > maximum {
		checker.add(path, fmt.Sprintf("Too big: expected at most %d", maximum))
	}
}

func (checker *previewBlueprintChecker) plainText(path []string, value string, maximum int) {
	checker.checkedText(path, value, 1, maximum)
}

func (checker *previewBlueprintChecker) checkedText(path []string, value string, minimum, maximum int) {
	length := previewBlueprintTextLength(value)
	if length < minimum {
		checker.add(path, fmt.Sprintf("Too small: expected at least %d characters", minimum))
	}
	if length > maximum {
		checker.add(path, fmt.Sprintf("Too big: expected at most %d characters", maximum))
	}
	if previewBlueprintControlCharacter.MatchString(value) {
		checker.add(path, "Control characters are not allowed")
	}
	if !utf8.ValidString(value) {
		checker.add(path, "Text must be valid Unicode")
	}
	if LooksLikePreviewBlueprintLiveCredential(value) {
		checker.add(path, previewBlueprintCredentialText)
	}
}

func (checker *previewBlueprintChecker) command(path []string, value string) {
	checker.plainText(path, value, previewBlueprintMaxCommandBytes)
	if len(value) > previewBlueprintMaxCommandBytes {
		checker.add(path, "Command is longer than 2 KB")
	}
}

func (checker *previewBlueprintChecker) pinnedImage(path []string, value string) {
	if previewBlueprintTextLength(value) > 512 {
		checker.add(path, "Too big: expected at most 512 characters")
	}
	if !previewBlueprintPinnedImage.MatchString(value) {
		checker.add(path, "Image must be pinned as <repository>@sha256:<digest>")
	}
	if previewBlueprintIsPrivateRegistry(value) {
		checker.add(path, "Image registry must be a public DNS name, not localhost or an IP address")
	}
}

func (checker *previewBlueprintChecker) healthPath(path []string, value string) {
	length := previewBlueprintTextLength(value)
	if length < 1 {
		checker.add(path, "Too small: expected at least 1 character")
	}
	if length > 512 {
		checker.add(path, "Too big: expected at most 512 characters")
	}
	if !strings.HasPrefix(value, "/") ||
		strings.Contains(value, "//") ||
		previewBlueprintControlCharacter.MatchString(value) ||
		!utf8.ValidString(value) ||
		previewBlueprintHealthPathForbidden.MatchString(value) ||
		previewBlueprintEncodedSeparator.MatchString(value) ||
		slices.Contains(strings.Split(value, "/"), "..") {
		checker.add(path, "Health path must start with a single / and have no query, fragment, .., //, backslash or encoded separator")
	}
}

func previewBlueprintIsHostname(value string) bool {
	length := previewBlueprintTextLength(value)
	return length >= 1 && length <= 253 && previewBlueprintHostname.MatchString(value)
}

func previewBlueprintRegistryHost(image string) (string, bool) {
	repository, _, _ := strings.Cut(image, "@")
	segments := strings.Split(repository, "/")
	if len(segments) < 2 {
		return "", false
	}
	first := segments[0]
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		return "", false
	}
	host, _, _ := strings.Cut(first, ":")
	return host, true
}

func previewBlueprintIsPrivateRegistry(image string) bool {
	host, ok := previewBlueprintRegistryHost(image)
	if !ok {
		return false
	}
	return !previewBlueprintIsPublicHost(host)
}

func previewBlueprintIsPublicHost(host string) bool {
	return previewBlueprintIsHostname(host) && !previewBlueprintInternalHost.MatchString(host)
}
