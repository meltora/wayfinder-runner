package remoteconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/apikey"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	LocalFileName  = "runner.local.yaml"
	RemoteFileName = "runner.remote.json"
)

var ErrNotConfigured = errors.New("this runner has no saved configuration; run our-runner configure first")

type Local struct {
	ControlPlaneURL     string                        `yaml:"control_plane_url"`
	StateDir            string                        `yaml:"state_dir"`
	ProviderEnvironment map[string]map[string]string  `yaml:"provider_environment,omitempty"`
	ProviderAPIKeys     map[string]apikey.Source      `yaml:"provider_api_keys,omitempty"`
	ProviderBinaries    map[string]string             `yaml:"provider_binaries,omitempty"`
	AcknowledgeDirect   bool                          `yaml:"acknowledge_direct,omitempty"`
	Checks              map[string]config.CheckConfig `yaml:"checks,omitempty"`
	PreviewHosting      config.PreviewHostingConfig   `yaml:"preview_hosting,omitempty"`
}

type localDocument struct {
	Local                       `yaml:",inline"`
	AcceptedPackKeyFingerprints yaml.Node `yaml:"accepted_pack_key_fingerprints"`
}

func (l Local) Validate() error {
	if _, err := ConfigurationURL(l.ControlPlaneURL); err != nil {
		return fmt.Errorf("control_plane_url: %w", err)
	}
	if !filepath.IsAbs(l.StateDir) {
		return fmt.Errorf("state_dir must be an absolute path")
	}
	for provider, binary := range l.ProviderBinaries {
		if binary != "" && !filepath.IsAbs(binary) {
			return fmt.Errorf("provider binary for %s must be an absolute path", provider)
		}
	}
	return nil
}

func ConfigurationURL(gatewayURL string) (string, error) {
	parsed, err := url.Parse(gatewayURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("gateway URL must be an absolute wss:// URL")
	}
	if parsed.Scheme != "wss" {
		return "", fmt.Errorf("gateway URL must use wss://")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("gateway URL must not contain credentials, query parameters, or fragments")
	}
	base := parsed.Path
	if index := strings.Index(base, "/v1/"); index >= 0 {
		base = base[:index]
	}
	base = strings.TrimSuffix(base, "/")
	return (&url.URL{Scheme: "https", Host: parsed.Host, Path: base + "/v1/runners/configuration"}).String(), nil
}

func LoadLocal(stateDir string) (Local, error) {
	var document localDocument
	out := document.Local
	raw, err := safeio.ReadRegularFile(filepath.Join(stateDir, LocalFileName), 1<<20, 0o077)
	if errors.Is(err, fs.ErrNotExist) {
		return out, ErrNotConfigured
	}
	if err != nil {
		return out, fmt.Errorf("read %s: %w", LocalFileName, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return out, fmt.Errorf("parse %s: %w", LocalFileName, err)
	}
	out = document.Local
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("parse %s: multiple YAML documents are not allowed", LocalFileName)
	}
	if filepath.Clean(out.StateDir) != filepath.Clean(stateDir) {
		return out, fmt.Errorf("%s belongs to state directory %s", LocalFileName, out.StateDir)
	}
	if err := out.Validate(); err != nil {
		return out, fmt.Errorf("%s: %w", LocalFileName, err)
	}
	return out, nil
}

func LoadCachedDelivery(stateDir string) ([]byte, error) {
	raw, err := safeio.ReadRegularFile(filepath.Join(stateDir, RemoteFileName), MaxDeliveryBytes, 0o077)
	if err != nil {
		return nil, fmt.Errorf("read cached remote configuration: %w", err)
	}
	return raw, nil
}

func SaveFiles(stateDir string, local Local, remote []byte) error {
	encoded, err := yaml.Marshal(local)
	if err != nil {
		return err
	}
	if err := SaveCachedDelivery(stateDir, remote); err != nil {
		return err
	}
	return safeio.AtomicWriteFile(stateDir, LocalFileName, encoded, 0o600)
}

func SaveCachedDelivery(stateDir string, remote []byte) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return safeio.AtomicWriteFile(stateDir, RemoteFileName, remote, 0o600)
}
