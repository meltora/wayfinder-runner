package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example/app-runner/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	MaxAgentPackPublicKeys            = 128
	MaxSourcePublicationInstallations = 128
)

type Config struct {
	ControlPlaneURL                string                                         `yaml:"control_plane_url"`
	ControlPlaneHTTPURL            string                                         `yaml:"control_plane_http_url,omitempty"`
	StateDir                       string                                         `yaml:"state_dir"`
	Transport                      string                                         `yaml:"transport"`
	Isolation                      string                                         `yaml:"isolation,omitempty"`
	AllowUnsafeDirect              bool                                           `yaml:"allow_unsafe_direct,omitempty"`
	Supervisor                     string                                         `yaml:"supervisor,omitempty"`
	Provider                       string                                         `yaml:"provider,omitempty"`
	AgentModel                     string                                         `yaml:"agent_model,omitempty"`
	ProviderBinaries               map[string]string                              `yaml:"provider_binaries,omitempty"`
	JobImage                       string                                         `yaml:"job_image,omitempty"`
	JobMemoryMB                    int                                            `yaml:"job_memory_mb,omitempty"`
	JobCPUs                        float64                                        `yaml:"job_cpus,omitempty"`
	JobPIDs                        int                                            `yaml:"job_pids,omitempty"`
	ArtifactSpoolMB                int                                            `yaml:"artifact_spool_mb,omitempty"`
	JournalRetentionDays           int                                            `yaml:"journal_retention_days,omitempty"`
	JournalMaxMB                   int                                            `yaml:"journal_max_mb,omitempty"`
	JobNetwork                     string                                         `yaml:"job_network,omitempty"`
	JobNetworkName                 string                                         `yaml:"job_network_name,omitempty"`
	EgressPolicyID                 string                                         `yaml:"egress_policy_id,omitempty"`
	UpdateSocketPath               string                                         `yaml:"update_socket_path,omitempty"`
	AgentPackPublicKeys            map[string]string                              `yaml:"agent_pack_public_keys,omitempty"`
	AllowedArtifactOrigins         []string                                       `yaml:"allowed_artifact_origins,omitempty"`
	Repositories                   map[string]RepositoryConfig                    `yaml:"repositories,omitempty"`
	Checks                         map[string]CheckConfig                         `yaml:"checks,omitempty"`
	SourcePublicationInstallations map[string]SourcePublicationInstallationConfig `yaml:"source_publication_installations,omitempty"`
	Labels                         map[string]string                              `yaml:"labels,omitempty"`
}

type RepositoryConfig struct {
	URL string `yaml:"url"`
}

type CheckConfig struct {
	Command        string   `yaml:"command"`
	Args           []string `yaml:"args,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty"`
	RepositoryID   string   `yaml:"repository_id,omitempty"`
}

// SourcePublicationInstallationConfig binds a control-plane installation ID
// to credentials owned by this runner. Tokens never cross the runner protocol.
type SourcePublicationInstallationConfig struct {
	Provider   string `yaml:"provider"`
	APIBaseURL string `yaml:"api_base_url,omitempty"`
	TokenFile  string `yaml:"token_file"`
}

func Default() Config {
	return Config{
		StateDir:             defaultStateDir(),
		Transport:            "websocket",
		Isolation:            "docker",
		Supervisor:           "auto",
		Provider:             "codex",
		JobMemoryMB:          4096,
		JobCPUs:              2,
		JobPIDs:              512,
		ArtifactSpoolMB:      1024,
		JournalRetentionDays: 30,
		JournalMaxMB:         512,
		JobNetwork:           "none",
		UpdateSocketPath:     "/run/our-runner-updater/control.sock",
	}
}

// Normalize applies defaults without mutating the caller's value.
func Normalize(in Config) Config {
	out := in
	defaults := Default()
	if out.StateDir == "" {
		out.StateDir = defaults.StateDir
	}
	if out.Transport == "" {
		out.Transport = defaults.Transport
	}
	if out.Isolation == "" {
		out.Isolation = defaults.Isolation
	}
	if out.Supervisor == "" {
		out.Supervisor = defaults.Supervisor
	}
	if out.Provider == "" {
		out.Provider = defaults.Provider
	}
	if out.JobMemoryMB == 0 {
		out.JobMemoryMB = defaults.JobMemoryMB
	}
	if out.JobCPUs == 0 {
		out.JobCPUs = defaults.JobCPUs
	}
	if out.JobPIDs == 0 {
		out.JobPIDs = defaults.JobPIDs
	}
	if out.ArtifactSpoolMB == 0 {
		out.ArtifactSpoolMB = defaults.ArtifactSpoolMB
	}
	if out.JournalRetentionDays == 0 {
		out.JournalRetentionDays = defaults.JournalRetentionDays
	}
	if out.JournalMaxMB == 0 {
		out.JournalMaxMB = defaults.JournalMaxMB
	}
	if out.JobNetwork == "" {
		out.JobNetwork = defaults.JobNetwork
	}
	if out.UpdateSocketPath == "" {
		out.UpdateSocketPath = defaults.UpdateSocketPath
	}
	if in.ProviderBinaries != nil {
		out.ProviderBinaries = make(map[string]string, len(in.ProviderBinaries))
		for key, value := range in.ProviderBinaries {
			out.ProviderBinaries[key] = value
		}
	}
	if in.Labels != nil {
		out.Labels = make(map[string]string, len(in.Labels))
		for key, value := range in.Labels {
			out.Labels[key] = value
		}
	}
	if in.AgentPackPublicKeys != nil {
		out.AgentPackPublicKeys = make(map[string]string, len(in.AgentPackPublicKeys))
		for key, value := range in.AgentPackPublicKeys {
			out.AgentPackPublicKeys[key] = value
		}
	}
	if in.Repositories != nil {
		out.Repositories = make(map[string]RepositoryConfig, len(in.Repositories))
		for key, value := range in.Repositories {
			out.Repositories[key] = value
		}
	}
	if in.Checks != nil {
		out.Checks = make(map[string]CheckConfig, len(in.Checks))
		for key, value := range in.Checks {
			copyValue := value
			copyValue.Args = append([]string(nil), value.Args...)
			out.Checks[key] = copyValue
		}
	}
	if in.SourcePublicationInstallations != nil {
		out.SourcePublicationInstallations = make(map[string]SourcePublicationInstallationConfig, len(in.SourcePublicationInstallations))
		for key, value := range in.SourcePublicationInstallations {
			copyValue := value
			if copyValue.APIBaseURL == "" {
				copyValue.APIBaseURL = defaultSourcePublicationAPIBaseURL(copyValue.Provider)
			}
			out.SourcePublicationInstallations[key] = copyValue
		}
	}
	out.AllowedArtifactOrigins = append([]string(nil), in.AllowedArtifactOrigins...)
	return out
}

func defaultStateDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "our-runner")
	}
	return filepath.Join(os.TempDir(), "our-runner")
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := safeio.ReadRegularFile(path, 4<<20, 0o027)
	if err != nil {
		return cfg, fmt.Errorf("read private config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cfg, fmt.Errorf("parse config: multiple YAML documents are not allowed")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.ControlPlaneURL == "" {
		return fmt.Errorf("control_plane_url is required")
	}
	if err := validateServiceURL(c.ControlPlaneURL, "wss", "ws", c.AllowUnsafeDirect); err != nil {
		return fmt.Errorf("control_plane_url: %w", err)
	}
	if c.ControlPlaneHTTPURL != "" {
		if err := validateServiceURL(c.ControlPlaneHTTPURL, "https", "http", c.AllowUnsafeDirect); err != nil {
			return fmt.Errorf("control_plane_http_url: %w", err)
		}
	}
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("state_dir must be an absolute path")
	}
	if strings.ContainsAny(c.StateDir, "?\x00") {
		return fmt.Errorf("state_dir contains unsupported characters")
	}
	cleanStateDir := filepath.Clean(c.StateDir)
	volumeRoot := filepath.VolumeName(cleanStateDir) + string(filepath.Separator)
	homeDir, _ := os.UserHomeDir()
	if cleanStateDir == volumeRoot || filepath.Dir(cleanStateDir) == volumeRoot || (homeDir != "" && cleanStateDir == filepath.Clean(homeDir)) {
		return fmt.Errorf("state_dir must be a dedicated subdirectory, not a filesystem root or user home")
	}
	if c.Transport != "websocket" {
		return fmt.Errorf("transport must be websocket; long-poll transport is not implemented")
	}
	switch c.Supervisor {
	case "", "auto", "direct", "herdr", "tmux":
	default:
		return fmt.Errorf("supervisor must be auto, direct, herdr, or tmux")
	}
	switch c.Isolation {
	case "docker":
		if !immutableImagePattern.MatchString(c.JobImage) {
			return fmt.Errorf("job_image must be pinned by sha256 digest for docker isolation")
		}
	case "direct":
		if !c.AllowUnsafeDirect {
			return fmt.Errorf("direct isolation is unsafe and requires allow_unsafe_direct: true")
		}
	default:
		return fmt.Errorf("isolation must be docker or direct")
	}
	if !supportedProvider(c.Provider) {
		return fmt.Errorf("provider must be codex, claude, or opencode")
	}
	if c.AgentModel != "" {
		if c.Provider != "opencode" {
			return fmt.Errorf("agent_model is currently supported only by the opencode provider")
		}
		if !validAgentModel(c.AgentModel) {
			return fmt.Errorf("agent_model must use provider/model format")
		}
	}
	for provider, binary := range c.ProviderBinaries {
		if !supportedProvider(provider) {
			return fmt.Errorf("provider binary %q is unsupported", provider)
		}
		if binary != "" && !filepath.IsAbs(binary) {
			return fmt.Errorf("provider binary for %s must be an absolute path", provider)
		}
	}
	if c.JobMemoryMB < 128 || c.JobCPUs <= 0 || c.JobPIDs < 16 {
		return fmt.Errorf("job resource limits are invalid")
	}
	if c.ArtifactSpoolMB < 256 || c.ArtifactSpoolMB > 10240 {
		return fmt.Errorf("artifact_spool_mb must be between 256 and 10240")
	}
	if c.JournalRetentionDays < 7 || c.JournalRetentionDays > 365 {
		return fmt.Errorf("journal_retention_days must be between 7 and 365")
	}
	if c.JournalMaxMB < 64 || c.JournalMaxMB > 4096 {
		return fmt.Errorf("journal_max_mb must be between 64 and 4096")
	}
	if c.JobNetwork != "none" && c.JobNetwork != "restricted" {
		return fmt.Errorf("job_network must be none or restricted")
	}
	if c.JobNetwork == "restricted" {
		if !idPattern.MatchString(c.JobNetworkName) || !idPattern.MatchString(c.EgressPolicyID) {
			return fmt.Errorf("restricted job network requires valid job_network_name and egress_policy_id")
		}
	} else if c.JobNetworkName != "" || c.EgressPolicyID != "" {
		return fmt.Errorf("job_network_name and egress_policy_id require job_network: restricted")
	}
	if !filepath.IsAbs(c.UpdateSocketPath) || filepath.Clean(c.UpdateSocketPath) != c.UpdateSocketPath ||
		len(c.UpdateSocketPath) > 100 || strings.ContainsRune(c.UpdateSocketPath, '\x00') {
		return fmt.Errorf("update_socket_path must be a clean absolute Unix socket path of at most 100 bytes")
	}
	if len(c.AgentPackPublicKeys) > MaxAgentPackPublicKeys {
		return fmt.Errorf("agent_pack_public_keys must contain at most %d keys", MaxAgentPackPublicKeys)
	}
	for keyID, encoded := range c.AgentPackPublicKeys {
		if !idPattern.MatchString(keyID) || len(keyID) > 128 {
			return fmt.Errorf("agent pack key ID %q is invalid", keyID)
		}
		if err := validateEd25519PublicKey(encoded); err != nil {
			return fmt.Errorf("agent pack key %s: %w", keyID, err)
		}
	}
	for _, origin := range c.AllowedArtifactOrigins {
		if err := validateArtifactOrigin(origin); err != nil {
			return fmt.Errorf("allowed_artifact_origin: %w", err)
		}
	}
	for id, repository := range c.Repositories {
		if len(id) > 128 || id == "." || id == ".." || !idPattern.MatchString(id) {
			return fmt.Errorf("repository ID %q is invalid", id)
		}
		if err := validateRepositoryURL(repository.URL, c.AllowUnsafeDirect); err != nil {
			return fmt.Errorf("repository %s: %w", id, err)
		}
	}
	for id, check := range c.Checks {
		if len(id) > 128 || id == "." || id == ".." || !idPattern.MatchString(id) {
			return fmt.Errorf("check ID %q is invalid", id)
		}
		if check.Command == "" || len(check.Command) > 512 || len(check.Args) > 128 {
			return fmt.Errorf("check %s has an invalid command", id)
		}
		if check.TimeoutSeconds < 0 || check.TimeoutSeconds > 1800 {
			return fmt.Errorf("check %s timeout must be between 0 and 1800 seconds", id)
		}
		if check.RepositoryID != "" {
			if _, ok := c.Repositories[check.RepositoryID]; !ok {
				return fmt.Errorf("check %s references unregistered repository %s", id, check.RepositoryID)
			}
		}
	}
	if len(c.SourcePublicationInstallations) > MaxSourcePublicationInstallations {
		return fmt.Errorf("source_publication_installations must contain at most %d entries", MaxSourcePublicationInstallations)
	}
	for id, installation := range c.SourcePublicationInstallations {
		if len(id) > 128 || id == "." || id == ".." || !idPattern.MatchString(id) {
			return fmt.Errorf("source publication installation ID %q is invalid", id)
		}
		if installation.Provider != "github" && installation.Provider != "gitlab" {
			return fmt.Errorf("source publication installation %s uses unsupported provider %q", id, installation.Provider)
		}
		apiBaseURL := installation.APIBaseURL
		if apiBaseURL == "" {
			apiBaseURL = defaultSourcePublicationAPIBaseURL(installation.Provider)
		}
		if err := validateSourcePublicationAPIBaseURL(apiBaseURL); err != nil {
			return fmt.Errorf("source publication installation %s API base URL: %w", id, err)
		}
		if !filepath.IsAbs(installation.TokenFile) || filepath.Clean(installation.TokenFile) != installation.TokenFile ||
			strings.ContainsRune(installation.TokenFile, '\x00') {
			return fmt.Errorf("source publication installation %s token_file must be a clean absolute path", id)
		}
	}
	return nil
}

func defaultSourcePublicationAPIBaseURL(provider string) string {
	switch provider {
	case "github":
		return "https://api.github.com"
	case "gitlab":
		return "https://gitlab.com/api/v4"
	default:
		return ""
	}
}

func validateSourcePublicationAPIBaseURL(raw string) error {
	if err := validateServiceURL(raw, "https", "", false); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(parsed.Hostname(), ".") ||
		parsed.RawPath != "" || parsed.ForceQuery || (parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path) ||
		(parsed.Path != "" && parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/")) {
		return fmt.Errorf("must be a canonical HTTPS API base URL")
	}
	return nil
}

func validateArtifactOrigin(raw string) error {
	if err := validateServiceURL(raw, "https", "", false); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if (parsed.Path != "" && parsed.Path != "/") || parsed.Host != strings.ToLower(parsed.Host) ||
		strings.HasSuffix(parsed.Hostname(), ".") || parsed.Port() == "443" {
		return fmt.Errorf("must be a canonical HTTPS origin without a path")
	}
	for _, value := range []byte(parsed.Host) {
		if value >= 0x80 {
			return fmt.Errorf("hostname must use canonical ASCII encoding")
		}
	}
	return nil
}

func supportedProvider(provider string) bool {
	return provider == "codex" || provider == "claude" || provider == "opencode"
}

func validAgentModel(model string) bool {
	if len(model) == 0 || len(model) > 256 || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\r\n\x00") {
		return false
	}
	provider, modelID, ok := strings.Cut(model, "/")
	return ok && provider != "" && len(provider) <= 128 && modelID != "" && !strings.HasPrefix(modelID, "/")
}

func validateEd25519PublicKey(encoded string) error {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(der) != encoded {
		return fmt.Errorf("invalid canonical base64")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return fmt.Errorf("invalid PKIX public key")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must use Ed25519")
	}
	return nil
}

func validateServiceURL(raw, secureScheme, developmentScheme string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("must be an absolute URL")
	}
	if u.Scheme != secureScheme && (!allowInsecure || u.Scheme != developmentScheme) {
		return fmt.Errorf("must use %s", secureScheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("must not contain credentials, query parameters, or fragments")
	}
	return nil
}

var immutableImagePattern = regexp.MustCompile(`^[^\s@]+@sha256:[a-fA-F0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateRepositoryURL(raw string, allowLocal bool) error {
	if raw == "" {
		return fmt.Errorf("URL is required")
	}
	if filepath.IsAbs(raw) {
		if allowLocal {
			return nil
		}
		return fmt.Errorf("local repository paths are disabled")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return fmt.Errorf("URL must use HTTPS or SSH")
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("URL must not contain a query or fragment")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return fmt.Errorf("URL must not embed credentials")
		}
	}
	return nil
}
