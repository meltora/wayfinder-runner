package agentpack

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxPackPathBytes = 512
	maxPackPathDepth = 16
)

// MountAuthenticatedArchive validates an archive whose exact byte digest has
// already been authenticated by the signed delivery authority. The legacy
// in-archive signature is treated as format metadata; the outer Ed25519
// attestation is the delivery trust boundary.
func MountAuthenticatedArchive(archive io.Reader, destination string) (*Pack, error) {
	return MountAuthenticatedArchiveWithPolicy(archive, destination, MountPolicy{})
}

type MountPolicy struct {
	Version  string
	Provider string
}

func MountAuthenticatedArchiveWithPolicy(archive io.Reader, destination string, policy MountPolicy) (*Pack, error) {
	if archive == nil {
		return nil, fmt.Errorf("Agent Pack archive is required")
	}
	manifestBytes, files, err := readAuthenticatedArchive(archive)
	if err != nil {
		return nil, err
	}
	manifest, err := validateAuthenticatedManifest(manifestBytes, files)
	if err != nil {
		return nil, err
	}
	if policy.Version != "" && manifest.Version != policy.Version {
		return nil, fmt.Errorf("Agent Pack version does not match signed reference")
	}
	if policy.Provider != "" && manifest.Provider != "" && canonicalProvider(manifest.Provider) != canonicalProvider(policy.Provider) {
		return nil, fmt.Errorf("Agent Pack manifest provider does not match signed authority")
	}
	if err := mountAuthenticatedFiles(destination, files); err != nil {
		return nil, err
	}
	return &Pack{Manifest: manifest, Root: destination}, nil
}

func readAuthenticatedArchive(archive io.Reader) ([]byte, map[string][]byte, error) {
	buffered := bufio.NewReader(archive)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return nil, nil, fmt.Errorf("open Agent Pack gzip stream: %w", err)
	}
	gz.Multistream(false)
	tape := tar.NewReader(gz)
	files := map[string][]byte{}
	seenFolded := map[string]string{}
	var manifestBytes []byte
	var signatureSeen bool
	var totalSize int64
	entryCount := 0
	for {
		header, nextErr := tape.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("read Agent Pack tar stream: %w", nextErr)
		}
		entryCount++
		if entryCount > maxPackFiles+2 {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack exceeds entry-count limit")
		}
		name := filepath.ToSlash(header.Name)
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack contains unsupported entry type")
		}
		if !validAuthenticatedPackPath(name) {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack contains an unsafe or ambiguous path")
		}
		folded := strings.ToLower(name)
		if previous, exists := seenFolded[folded]; exists {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack path collides with %q", previous)
		}
		seenFolded[folded] = name
		if header.Mode&0o7000 != 0 || hasSparseMetadata(header.PAXRecords) {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack entry uses dangerous mode or sparse metadata")
		}
		if header.Size < 0 || header.Size > maxPackFileSize {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack entry exceeds file-size limit")
		}
		totalSize += header.Size
		if totalSize > maxPackTotalSize {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("Agent Pack exceeds expanded-size limit")
		}
		data, readErr := io.ReadAll(io.LimitReader(tape, maxPackFileSize+1))
		if readErr != nil || int64(len(data)) != header.Size {
			_ = gz.Close()
			return nil, nil, fmt.Errorf("read Agent Pack entry")
		}
		switch name {
		case "pack.yaml":
			manifestBytes = data
		case "signature":
			signatureSeen = true
			if err := validateLegacySignatureMetadata(data); err != nil {
				_ = gz.Close()
				return nil, nil, err
			}
		default:
			if len(files) >= maxPackFiles {
				_ = gz.Close()
				return nil, nil, fmt.Errorf("Agent Pack exceeds file-count limit")
			}
			files[name] = data
		}
	}
	if err := gz.Close(); err != nil {
		return nil, nil, fmt.Errorf("close Agent Pack gzip stream: %w", err)
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		return nil, nil, fmt.Errorf("Agent Pack archive contains trailing payload or multiple gzip members")
	}
	if manifestBytes == nil || !signatureSeen {
		return nil, nil, fmt.Errorf("Agent Pack archive is missing required metadata")
	}
	if err := rejectFileDirectoryCollisions(files); err != nil {
		return nil, nil, err
	}
	return manifestBytes, files, nil
}

func validateAuthenticatedManifest(manifestBytes []byte, files map[string][]byte) (Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse Agent Pack manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("Agent Pack manifest contains multiple YAML documents")
	}
	if manifest.Version == "" || len(manifest.Version) > 128 ||
		(manifest.Provider != "" && manifest.Provider != "codex" && manifest.Provider != "claude" &&
			manifest.Provider != "claude-code" && manifest.Provider != "opencode") {
		return Manifest{}, fmt.Errorf("Agent Pack manifest has invalid version or provider")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxPackFiles || len(manifest.Files) != len(files) {
		return Manifest{}, fmt.Errorf("Agent Pack manifest file count does not match archive")
	}
	claimedDigest := manifest.Digest
	if !validSHA256(claimedDigest) {
		return Manifest{}, fmt.Errorf("Agent Pack manifest has invalid digest")
	}
	manifest.Digest = ""
	unsignedBytes, err := yaml.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("canonicalize Agent Pack manifest: %w", err)
	}
	contentSum := sha256.Sum256(unsignedBytes)
	if hex.EncodeToString(contentSum[:]) != claimedDigest {
		return Manifest{}, fmt.Errorf("Agent Pack manifest digest mismatch")
	}
	manifest.Digest = claimedDigest
	for name, expectedDigest := range manifest.Files {
		if !validAuthenticatedPackPath(name) || name == "pack.yaml" || name == "signature" || !validSHA256(expectedDigest) {
			return Manifest{}, fmt.Errorf("Agent Pack manifest contains an invalid file declaration")
		}
		contents, ok := files[name]
		if !ok {
			return Manifest{}, fmt.Errorf("Agent Pack manifest references a missing file")
		}
		sum := sha256.Sum256(contents)
		if hex.EncodeToString(sum[:]) != expectedDigest {
			return Manifest{}, fmt.Errorf("Agent Pack file digest mismatch")
		}
	}
	return manifest, nil
}

func mountAuthenticatedFiles(destination string, files map[string][]byte) (err error) {
	if err := prepareDestination(destination); err != nil {
		return fmt.Errorf("prepare Agent Pack job view: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(destination)
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	directories := map[string]bool{".": true}
	for _, relative := range names {
		name := filepath.FromSlash(relative)
		directory := filepath.Dir(name)
		if err := root.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		for current := directory; current != "."; current = filepath.Dir(current) {
			directories[current] = true
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return err
		}
		if _, err := file.Write(files[relative]); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	dirs := make([]string, 0, len(directories))
	for directory := range directories {
		dirs = append(dirs, directory)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, directory := range dirs {
		if err := root.Chmod(directory, 0o555); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

func validAuthenticatedPackPath(name string) bool {
	if !validPackPath(name) || len(name) > maxPackPathBytes || strings.Count(name, "/") >= maxPackPathDepth {
		return false
	}
	for _, value := range []byte(name) {
		if value >= 0x80 || !(value >= 'a' && value <= 'z') && !(value >= 'A' && value <= 'Z') &&
			!(value >= '0' && value <= '9') && value != '.' && value != '_' && value != '-' && value != '/' {
			return false
		}
	}
	return true
}

func hasSparseMetadata(records map[string]string) bool {
	for key := range records {
		if strings.HasPrefix(strings.ToLower(key), "gnu.sparse") || strings.Contains(strings.ToLower(key), "sparse") {
			return true
		}
	}
	return false
}

func validateLegacySignatureMetadata(raw []byte) error {
	encoded := strings.TrimSpace(string(raw))
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > 512 || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return fmt.Errorf("Agent Pack contains invalid legacy signature metadata")
	}
	return nil
}

func rejectFileDirectoryCollisions(files map[string][]byte) error {
	folded := make(map[string]bool, len(files))
	for name := range files {
		folded[strings.ToLower(name)] = true
	}
	for name := range files {
		parts := strings.Split(strings.ToLower(name), "/")
		for index := 1; index < len(parts); index++ {
			if folded[strings.Join(parts[:index], "/")] {
				return fmt.Errorf("Agent Pack path is both a file and directory")
			}
		}
	}
	return nil
}
