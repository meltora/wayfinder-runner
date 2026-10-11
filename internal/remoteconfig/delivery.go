package remoteconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustset"
)

const MaxDeliveryBytes = 1 << 20

const MaxRevokedAgentPackDigests = 256

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	OmissionCloneURLUnavailable  = "clone_url_unavailable"
	OmissionInstallationInactive = "installation_inactive"
)

type OmittedRepository struct {
	ID     string
	Reason string
}

type Remote struct {
	Isolation                      string
	AllowUnsafeDirect              bool
	Provider                       string
	JobImage                       string
	JobNetwork                     string
	JobMemoryMB                    int
	JobCPUs                        float64
	JobPIDs                        int
	Labels                         map[string]string
	Repositories                   map[string]string
	SourcePublicationInstallations map[string]string
	RevokedAgentPackDigests        []string
	AgentPackTrustedKeySet         *v1.AgentPackTrustedKeySet
}

type Delivery struct {
	RunnerID            string
	ProfileRevision     int
	Digest              string
	Remote              Remote
	OmittedRepositories []OmittedRepository
}

var envelopeFields = []string{"schema_version", "runner_id", "profile_revision", "digest", "config", "pack_key_fingerprints", "omitted_repositories"}

var requiredEnvelopeFields = []string{"schema_version", "runner_id", "profile_revision", "digest", "config", "omitted_repositories"}

var remoteFields = map[string]bool{
	"isolation": true, "allow_unsafe_direct": true, "provider": true, "job_image": true, "job_network": true,
	"job_memory_mb": true, "job_cpus": true, "job_pids": true, "labels": true, "repositories": true,
	"agent_pack_public_keys": true, "source_publication_installations": true, "revoked_agent_pack_digests": true,
	"agent_pack_trusted_key_set": true,
}

var requiredRemoteFields = []string{"isolation", "provider", "labels", "repositories", "source_publication_installations"}

var trustedKeySetFields = []string{"document_base64", "signature_base64", "root_key_id"}

const maxTrustedKeySetSignatureBase64 = 128

func ParseDelivery(raw []byte, expectedRunnerID string) (Delivery, error) {
	var out Delivery
	if len(raw) > MaxDeliveryBytes {
		return out, fmt.Errorf("remote configuration exceeds %d bytes", MaxDeliveryBytes)
	}
	fields, err := objectFields(raw)
	if err != nil {
		return out, fmt.Errorf("remote configuration: %w", err)
	}
	if err := exactFields(fields, envelopeFields, requiredEnvelopeFields); err != nil {
		return out, fmt.Errorf("remote configuration: %w", err)
	}
	var schemaVersion int
	if err := strictValue(fields["schema_version"], &schemaVersion); err != nil || schemaVersion != 1 {
		return out, fmt.Errorf("remote configuration: unsupported schema_version")
	}
	if err := strictValue(fields["runner_id"], &out.RunnerID); err != nil || out.RunnerID != expectedRunnerID {
		return out, fmt.Errorf("remote configuration is for a different runner")
	}
	if err := strictValue(fields["profile_revision"], &out.ProfileRevision); err != nil || out.ProfileRevision < 1 {
		return out, fmt.Errorf("remote configuration: invalid profile_revision")
	}
	if err := strictValue(fields["digest"], &out.Digest); err != nil || !digestPattern.MatchString(out.Digest) {
		return out, fmt.Errorf("remote configuration: invalid digest")
	}
	remote, err := parseRemote(fields["config"])
	if err != nil {
		return out, fmt.Errorf("remote configuration: %w", err)
	}
	out.Remote = remote
	if value, ok := fields["pack_key_fingerprints"]; ok {
		if _, err := stringMap(value); err != nil {
			return out, fmt.Errorf("remote configuration: pack_key_fingerprints: %w", err)
		}
	}
	if out.OmittedRepositories, err = omittedRepositories(fields["omitted_repositories"]); err != nil {
		return out, fmt.Errorf("remote configuration: %w", err)
	}
	return out, nil
}

func parseRemote(raw json.RawMessage) (Remote, error) {
	var out Remote
	fields, err := objectFields(raw)
	if err != nil {
		return out, fmt.Errorf("config: %w", err)
	}
	for name := range fields {
		if !remoteFields[name] {
			return out, fmt.Errorf("config contains key %q, which the control plane does not own", name)
		}
	}
	for _, name := range requiredRemoteFields {
		if _, ok := fields[name]; !ok {
			return out, fmt.Errorf("config is missing %s", name)
		}
	}
	for name, target := range map[string]any{
		"isolation": &out.Isolation, "provider": &out.Provider, "job_image": &out.JobImage, "job_network": &out.JobNetwork,
		"job_memory_mb": &out.JobMemoryMB, "job_cpus": &out.JobCPUs, "job_pids": &out.JobPIDs, "allow_unsafe_direct": &out.AllowUnsafeDirect,
	} {
		if value, ok := fields[name]; ok {
			if err := strictValue(value, target); err != nil {
				return out, fmt.Errorf("config %s: %w", name, err)
			}
		}
	}
	_, hasJobImage := fields["job_image"]
	_, hasAllowUnsafeDirect := fields["allow_unsafe_direct"]
	switch out.Isolation {
	case "docker":
		if !hasJobImage || hasAllowUnsafeDirect {
			return out, fmt.Errorf("docker config requires job_image and must not set allow_unsafe_direct")
		}
	case "direct":
		if hasJobImage || !out.AllowUnsafeDirect {
			return out, fmt.Errorf("direct config requires allow_unsafe_direct: true and must not set job_image")
		}
	default:
		return out, fmt.Errorf("config isolation must be docker or direct")
	}
	if out.Labels, err = stringMap(fields["labels"]); err != nil {
		return out, fmt.Errorf("config labels: %w", err)
	}
	if value, ok := fields["agent_pack_public_keys"]; ok {
		if _, err := stringMap(value); err != nil {
			return out, fmt.Errorf("config agent_pack_public_keys: %w", err)
		}
	}
	if value, ok := fields["agent_pack_trusted_key_set"]; ok {
		if out.AgentPackTrustedKeySet, err = trustedKeySet(value); err != nil {
			return out, fmt.Errorf("config agent_pack_trusted_key_set: %w", err)
		}
	}
	if out.Repositories, err = singleFieldMap(fields["repositories"], "url"); err != nil {
		return out, fmt.Errorf("config repositories: %w", err)
	}
	for id, raw := range out.Repositories {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "ssh") || parsed.Host == "" {
			return out, fmt.Errorf("config repository %s must use an HTTPS or SSH URL", id)
		}
	}
	if out.SourcePublicationInstallations, err = singleFieldMap(fields["source_publication_installations"], "provider"); err != nil {
		return out, fmt.Errorf("config source_publication_installations: %w", err)
	}
	for id, provider := range out.SourcePublicationInstallations {
		if provider != "github" {
			return out, fmt.Errorf("config source publication installation %s must be a GitHub installation", id)
		}
	}
	if value, ok := fields["revoked_agent_pack_digests"]; ok {
		if out.RevokedAgentPackDigests, err = revokedAgentPackDigests(value); err != nil {
			return out, fmt.Errorf("config revoked_agent_pack_digests: %w", err)
		}
	}
	return out, nil
}

func trustedKeySet(raw json.RawMessage) (*v1.AgentPackTrustedKeySet, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return nil, err
	}
	if err := exactFields(fields, trustedKeySetFields, trustedKeySetFields); err != nil {
		return nil, err
	}
	var out v1.AgentPackTrustedKeySet
	if strictValue(fields["document_base64"], &out.DocumentBase64) != nil ||
		strictValue(fields["signature_base64"], &out.SignatureBase64) != nil ||
		strictValue(fields["root_key_id"], &out.RootKeyID) != nil {
		return nil, fmt.Errorf("fields must be strings")
	}
	if out.DocumentBase64 == "" || len(out.DocumentBase64) > base64.StdEncoding.EncodedLen(trustset.MaxDocumentBytes) {
		return nil, fmt.Errorf("document_base64 must be a non-empty signed document within %d bytes", trustset.MaxDocumentBytes)
	}
	if out.SignatureBase64 == "" || len(out.SignatureBase64) > maxTrustedKeySetSignatureBase64 {
		return nil, fmt.Errorf("signature_base64 is invalid")
	}
	if !digestPattern.MatchString(out.RootKeyID) {
		return nil, fmt.Errorf("root_key_id must be a lowercase SHA-256 key id")
	}
	return &out, nil
}

func revokedAgentPackDigests(raw json.RawMessage) ([]string, error) {
	var digests []string
	if err := strictValue(raw, &digests); err != nil || digests == nil {
		return nil, fmt.Errorf("must be an array of strings")
	}
	if len(digests) > MaxRevokedAgentPackDigests {
		return nil, fmt.Errorf("must contain at most %d digests", MaxRevokedAgentPackDigests)
	}
	for index, digest := range digests {
		if !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("entry %d must be a lowercase SHA-256 digest", index)
		}
		if index > 0 && digests[index-1] >= digest {
			return nil, fmt.Errorf("must be strictly ascending without duplicates")
		}
	}
	return digests, nil
}

func knownOmissionReason(reason string) bool {
	return reason == OmissionCloneURLUnavailable || reason == OmissionInstallationInactive
}

func omittedRepositories(raw json.RawMessage) ([]OmittedRepository, error) {
	var items []json.RawMessage
	if err := strictValue(raw, &items); err != nil || items == nil {
		return nil, fmt.Errorf("omitted_repositories must be an array")
	}
	out := make([]OmittedRepository, 0, len(items))
	for _, item := range items {
		fields, err := objectFields(item)
		if err != nil {
			return nil, fmt.Errorf("omitted_repositories: %w", err)
		}
		if err := exactFields(fields, []string{"id", "reason"}, []string{"id", "reason"}); err != nil {
			return nil, fmt.Errorf("omitted_repositories: %w", err)
		}
		var entry OmittedRepository
		if strictValue(fields["id"], &entry.ID) != nil || strictValue(fields["reason"], &entry.Reason) != nil ||
			entry.ID == "" || !knownOmissionReason(entry.Reason) {
			return nil, fmt.Errorf("omitted_repositories entry is invalid")
		}
		out = append(out, entry)
	}
	return out, nil
}

func stringMap(raw json.RawMessage) (map[string]string, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fields))
	for name, value := range fields {
		var text string
		if err := strictValue(value, &text); err != nil {
			return nil, fmt.Errorf("%s must be a string", name)
		}
		out[name] = text
	}
	return out, nil
}

func singleFieldMap(raw json.RawMessage, field string) (map[string]string, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fields))
	for name, value := range fields {
		entry, err := objectFields(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := exactFields(entry, []string{field}, []string{field}); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var text string
		if err := strictValue(entry[field], &text); err != nil {
			return nil, fmt.Errorf("%s.%s must be a string", name, field)
		}
		out[name] = text
	}
	return out, nil
}

func exactFields(fields map[string]json.RawMessage, allowed, required []string) error {
	allow := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allow[name] = true
	}
	for name := range fields {
		if !allow[name] {
			return fmt.Errorf("unexpected field %q", name)
		}
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing field %q", name)
		}
	}
	return nil
}

func objectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON object")
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid JSON object")
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid JSON value for %q", name)
		}
		out[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("invalid JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	return out, nil
}

func strictValue(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	return nil
}
