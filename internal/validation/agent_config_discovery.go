package validation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	maxAgentConfigItems      = 512
	maxRepositoryPathBytes   = 1024
	maxDiscoveryEventPayload = 900 << 10
)

var (
	parserVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+:-]{0,127}$`)
	permissionPattern    = regexp.MustCompile(`^[a-z][a-z0-9_.:-]*$`)
	conflictPattern      = regexp.MustCompile(`^[a-z][a-z0-9_.:/-]*$`)
)

var validAgentConfigSourceTypes = map[v1.AgentConfigSourceType]bool{
	v1.AgentConfigRepositoryInstructions: true, v1.AgentConfigSkill: true,
	v1.AgentConfigCommand: true, v1.AgentConfigAgentDefinition: true,
	v1.AgentConfigProviderConfiguration: true, v1.AgentConfigMCPDeclaration: true,
	v1.AgentConfigHook: true, v1.AgentConfigDevelopmentInstruction: true,
	v1.AgentConfigPlatformManifest: true, v1.AgentConfigSymlink: true,
	v1.AgentConfigCredential: true, v1.AgentConfigAuthCache: true,
	v1.AgentConfigBinary: true, v1.AgentConfigExecutableExtension: true,
}

var unsafeAgentConfigSourceTypes = map[v1.AgentConfigSourceType]bool{
	v1.AgentConfigSymlink: true, v1.AgentConfigCredential: true,
	v1.AgentConfigAuthCache: true, v1.AgentConfigBinary: true,
}

var reviewedAgentConfigSourceTypes = map[v1.AgentConfigSourceType]bool{
	v1.AgentConfigProviderConfiguration: true, v1.AgentConfigMCPDeclaration: true,
	v1.AgentConfigHook: true, v1.AgentConfigPlatformManifest: true,
	v1.AgentConfigExecutableExtension: true,
}

func AgentConfigDiscoveryEvent(event v1.AgentConfigDiscoveryEvent) error {
	if event.Kind != v1.EventKindAgentConfigDiscovery {
		return fmt.Errorf("agent config discovery kind is invalid")
	}
	for field, value := range map[string]string{
		"scan_id": event.ScanID, "project_id": event.ProjectID, "repository_id": event.RepositoryID,
	} {
		if err := ID(field, value); err != nil {
			return err
		}
	}
	if err := GitRef("branch_ref", event.BranchRef); err != nil {
		return err
	}
	if err := GitCommitSHA("commit_sha", event.CommitSHA); err != nil {
		return err
	}
	if event.CatalogVersion != v1.AgentConfigCatalogVersion {
		return fmt.Errorf("unsupported agent config catalog version")
	}
	if !parserVersionPattern.MatchString(event.ParserVersion) {
		return fmt.Errorf("parser_version is not canonical")
	}
	if event.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at is required")
	}
	if len(event.Items) > maxAgentConfigItems {
		return fmt.Errorf("agent config discovery exceeds item limit")
	}
	previousPath := ""
	for index, item := range event.Items {
		if err := validateAgentConfigItem(index, item); err != nil {
			return err
		}
		if previousPath != "" && previousPath >= item.Path {
			return fmt.Errorf("agent config items must have unique paths in lexical order")
		}
		previousPath = item.Path
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode agent config discovery: %w", err)
	}
	if len(raw) > maxDiscoveryEventPayload {
		return fmt.Errorf("agent config discovery exceeds event payload limit")
	}
	return nil
}

func validateAgentConfigItem(index int, item v1.AgentConfigDiscoveryItem) error {
	prefix := fmt.Sprintf("items[%d]", index)
	if err := repositoryPath(prefix+".path", item.Path); err != nil {
		return err
	}
	if !validAgentConfigSourceTypes[item.SourceType] {
		return fmt.Errorf("%s.source_type is invalid", prefix)
	}
	if item.SizeBytes < 0 || uint64(item.SizeBytes) > maxJSONSafeInteger {
		return fmt.Errorf("%s.size_bytes is outside the JSON-safe range", prefix)
	}
	if !validClassification(item.Classification) || !validRisk(item.Risk) ||
		!validCompatibility(item.Compatibility) || !validDecision(item.Decision) {
		return fmt.Errorf("%s contains an invalid classification", prefix)
	}
	if item.Provider != "" && (strings.TrimSpace(item.Provider) != item.Provider || boundedText(prefix+".provider", item.Provider, 128, true) != nil) {
		return fmt.Errorf("%s.provider is invalid", prefix)
	}
	if err := sortedCanonicalValues(prefix+".requested_permissions", item.RequestedPermissions, 32, 128, permissionPattern); err != nil {
		return err
	}
	if err := sortedCanonicalValues(prefix+".conflicts", item.Conflicts, 32, 256, conflictPattern); err != nil {
		return err
	}
	if item.Reason != "" && (strings.TrimSpace(item.Reason) != item.Reason || boundedText(prefix+".reason", item.Reason, 1024, true) != nil) {
		return fmt.Errorf("%s.reason is invalid", prefix)
	}
	if item.Classification == v1.AgentConfigClassRejected {
		if item.DigestSHA256 != "" || item.Decision != v1.AgentConfigDecisionRejected {
			return fmt.Errorf("%s rejected inventory cannot include a digest and must be rejected", prefix)
		}
	} else if err := SHA256Digest(prefix+".digest_sha256", item.DigestSHA256); err != nil {
		return err
	}
	if unsafeAgentConfigSourceTypes[item.SourceType] && item.Classification != v1.AgentConfigClassRejected {
		return fmt.Errorf("%s unsafe source type must be rejected", prefix)
	}
	if item.Decision == v1.AgentConfigDecisionApproved {
		return fmt.Errorf("%s discovery cannot grant approval", prefix)
	}
	if item.Decision == v1.AgentConfigDecisionRejected && item.Classification != v1.AgentConfigClassRejected {
		return fmt.Errorf("%s rejected decision requires rejected inventory", prefix)
	}
	if item.Decision == v1.AgentConfigDecisionNotRequired &&
		(item.Classification != v1.AgentConfigClassReady || item.Risk != v1.AgentConfigRiskLow ||
			item.Compatibility != v1.AgentConfigCompatible || len(item.RequestedPermissions) != 0 ||
			len(item.Conflicts) != 0 || reviewedAgentConfigSourceTypes[item.SourceType]) {
		return fmt.Errorf("%s cannot bypass review", prefix)
	}
	return validateAgentConfigPackClass(prefix, item)
}

func validateAgentConfigPackClass(prefix string, item v1.AgentConfigDiscoveryItem) error {
	if item.PackClass == "" && item.PackReasonCode == "" {
		return nil
	}
	rule, known := v1.AgentConfigPackReasonRules[item.PackReasonCode]
	if !known || rule.Class != item.PackClass {
		return fmt.Errorf("%s pack class and reason code are inconsistent", prefix)
	}
	if rule.Rejected != (item.Classification == v1.AgentConfigClassRejected) {
		return fmt.Errorf("%s pack reason code does not match the rejection state", prefix)
	}
	for _, sourceType := range rule.SourceTypes {
		if sourceType == item.SourceType {
			return nil
		}
	}
	return fmt.Errorf("%s pack reason code does not apply to the source type", prefix)
}

func repositoryPath(field, value string) error {
	if value == "" || len(value) > maxRepositoryPathBytes || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s is not a canonical relative POSIX path", field)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%s is not a canonical relative POSIX path", field)
		}
	}
	return nil
}

func sortedCanonicalValues(field string, values []string, maxItems, maxBytes int, pattern *regexp.Regexp) error {
	if len(values) > maxItems {
		return fmt.Errorf("%s exceeds item limit", field)
	}
	for index, value := range values {
		if len(value) > maxBytes || !pattern.MatchString(value) || (index > 0 && values[index-1] >= value) {
			return fmt.Errorf("%s must contain sorted unique canonical values", field)
		}
	}
	return nil
}

func validClassification(value v1.AgentConfigClassification) bool {
	return value == v1.AgentConfigClassReady || value == v1.AgentConfigClassWarning ||
		value == v1.AgentConfigClassBlocked || value == v1.AgentConfigClassReview || value == v1.AgentConfigClassRejected
}

func validRisk(value v1.AgentConfigRisk) bool {
	return value == v1.AgentConfigRiskLow || value == v1.AgentConfigRiskMedium ||
		value == v1.AgentConfigRiskHigh || value == v1.AgentConfigRiskCritical
}

func validCompatibility(value v1.AgentConfigCompatibility) bool {
	return value == v1.AgentConfigCompatible || value == v1.AgentConfigDegraded ||
		value == v1.AgentConfigIncompatible || value == v1.AgentConfigUnknown
}

func validDecision(value v1.AgentConfigDecision) bool {
	return value == v1.AgentConfigDecisionPending || value == v1.AgentConfigDecisionApproved ||
		value == v1.AgentConfigDecisionRejected || value == v1.AgentConfigDecisionBlocked ||
		value == v1.AgentConfigDecisionNotRequired
}
