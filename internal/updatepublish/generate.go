package updatepublish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example/app-runner/internal/updatecatalog"
)

type Options struct {
	DistRoot   string
	BaseURL    string
	Channel    string
	Sequence   uint64
	ReleaseID  string
	Version    string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	MinRunner  string
	MinUpdater string
}

type goreleaserArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	Extra  struct {
		Format   string `json:"Format"`
		ID       string `json:"ID"`
		Checksum string `json:"Checksum"`
	} `json:"extra"`
}

func Generate(rawArtifacts []byte, options Options) (updatecatalog.Manifest, error) {
	base, err := validateBaseURL(options.BaseURL)
	if err != nil {
		return updatecatalog.Manifest{}, err
	}
	if options.DistRoot == "" {
		return updatecatalog.Manifest{}, fmt.Errorf("dist root is required")
	}
	var records []goreleaserArtifact
	decoder := json.NewDecoder(bytes.NewReader(rawArtifacts))
	if err := decoder.Decode(&records); err != nil {
		return updatecatalog.Manifest{}, fmt.Errorf("decode GoReleaser artifacts: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return updatecatalog.Manifest{}, err
	}

	expected := map[string]string{
		"amd64": fmt.Sprintf("our-runner-update_%s_linux_amd64.tar.gz", options.Version),
		"arm64": fmt.Sprintf("our-runner-update_%s_linux_arm64.tar.gz", options.Version),
	}
	found := make(map[string]updatecatalog.Artifact, len(expected))
	for _, record := range records {
		if record.Type != "Archive" || record.GOOS != "linux" {
			continue
		}
		wantName, supported := expected[record.GOARCH]
		if !supported || record.Extra.ID != "linux-update" {
			continue
		}
		if record.Name != wantName || filepath.Base(record.Path) != wantName {
			return updatecatalog.Manifest{}, fmt.Errorf("unexpected %s archive name %q", record.GOARCH, record.Name)
		}
		if _, duplicate := found[record.GOARCH]; duplicate {
			return updatecatalog.Manifest{}, fmt.Errorf("duplicate linux/%s archive", record.GOARCH)
		}
		digest, size, err := digestRegularContainedFile(options.DistRoot, record.Path)
		if err != nil {
			return updatecatalog.Manifest{}, fmt.Errorf("archive %s: %w", record.Name, err)
		}
		if record.Extra.Format != "tar.gz" {
			return updatecatalog.Manifest{}, fmt.Errorf("archive %s has unsupported format %q", record.Name, record.Extra.Format)
		}
		if record.Extra.Checksum != "sha256:"+digest {
			return updatecatalog.Manifest{}, fmt.Errorf("archive %s checksum does not match GoReleaser metadata", record.Name)
		}
		found[record.GOARCH] = updatecatalog.Artifact{
			OS: "linux", Arch: record.GOARCH, Format: "tar.gz",
			URL: base + "/" + url.PathEscape(record.Name), SHA256: digest, Size: size,
		}
	}

	artifacts := make([]updatecatalog.Artifact, 0, len(expected))
	for arch := range expected {
		artifact, ok := found[arch]
		if !ok {
			return updatecatalog.Manifest{}, fmt.Errorf("release is missing linux/%s archive", arch)
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].OS != artifacts[j].OS {
			return artifacts[i].OS < artifacts[j].OS
		}
		return artifacts[i].Arch < artifacts[j].Arch
	})
	manifest := updatecatalog.Manifest{
		Schema: updatecatalog.SchemaV2, Channel: options.Channel, Sequence: options.Sequence,
		ReleaseID: options.ReleaseID, Version: options.Version,
		IssuedAt: options.IssuedAt.UTC(), ExpiresAt: options.ExpiresAt.UTC(),
		MinRunner: options.MinRunner, MinUpdater: options.MinUpdater, Artifacts: artifacts,
	}
	if _, err := updatecatalog.CanonicalPayload(manifest); err != nil {
		return updatecatalog.Manifest{}, fmt.Errorf("validate update manifest: %w", err)
	}
	return manifest, nil
}

func validateBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("base URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return "", fmt.Errorf("base URL must not contain credentials, query, or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func digestRegularContainedFile(root, path string) (string, int64, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", 0, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", 0, fmt.Errorf("path escapes distribution root")
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", 0, fmt.Errorf("path must be a regular non-symlink file")
	}
	// #nosec G304 -- absPath was constrained to absRoot and lstat-validated as
	// a non-symlink regular file immediately above.
	file, err := os.Open(absPath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	if written != info.Size() {
		return "", 0, fmt.Errorf("file size changed while hashing")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() {
		return "", 0, fmt.Errorf("file changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("GoReleaser artifacts contain trailing JSON")
		}
		return fmt.Errorf("decode trailing GoReleaser artifacts: %w", err)
	}
	return nil
}
