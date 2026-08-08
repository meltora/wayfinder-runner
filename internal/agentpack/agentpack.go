package agentpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example/app-runner/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	maxPackFiles     = 1024
	maxPackFileSize  = 1 << 20
	maxPackTotalSize = 64 << 20
)

type Manifest struct {
	Version  string            `yaml:"version"`
	Digest   string            `yaml:"digest"`
	Provider string            `yaml:"provider,omitempty"`
	Files    map[string]string `yaml:"files"`
}

type Pack struct {
	Manifest Manifest
	Root     string
}

func PackDir(srcDir, version, provider string, out io.Writer, key *ecdsa.PrivateKey) (string, error) {
	if key == nil || out == nil {
		return "", fmt.Errorf("signing key and output are required")
	}
	m := Manifest{Version: version, Provider: provider, Files: map[string]string{}}
	files := map[string][]byte{}
	totalSize := int64(0)
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return "", fmt.Errorf("open agent pack source: %w", err)
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("agent pack source %s is not a regular file", rel)
		}
		if info.Size() > maxPackFileSize {
			return fmt.Errorf("agent pack source %s exceeds file size limit", rel)
		}
		if len(m.Files) >= maxPackFiles {
			return fmt.Errorf("agent pack exceeds file-count limit")
		}
		file, err := root.Open(rel)
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		data, readErr := io.ReadAll(io.LimitReader(file, maxPackFileSize+1))
		_ = file.Close()
		if statErr != nil || readErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			return fmt.Errorf("agent pack source %s changed while being read", rel)
		}
		if len(data) > maxPackFileSize {
			return fmt.Errorf("agent pack source %s exceeds file size limit", rel)
		}
		totalSize += int64(len(data))
		if totalSize > maxPackTotalSize {
			return fmt.Errorf("agent pack exceeds expanded size limit")
		}
		if !validPackPath(filepath.ToSlash(rel)) {
			return fmt.Errorf("agent pack source path %s is invalid", rel)
		}
		if _, exists := files[filepath.ToSlash(rel)]; exists {
			return fmt.Errorf("duplicate agent pack source path %s", rel)
		}
		sum := sha256.Sum256(data)
		name := filepath.ToSlash(rel)
		m.Files[name] = hex.EncodeToString(sum[:])
		files[name] = data
		return nil
	})
	if err != nil {
		return "", err
	}
	m.Digest = ""
	unsignedBytes, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	contentSum := sha256.Sum256(unsignedBytes)
	m.Digest = hex.EncodeToString(contentSum[:])
	manifestBytes, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	signSum := sha256.Sum256(manifestBytes)
	sig, err := ecdsa.SignASN1(rand.Reader, key, signSum[:])
	if err != nil {
		return "", err
	}

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	writeEntry := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := writeEntry("pack.yaml", manifestBytes, 0o444); err != nil {
		return "", err
	}
	if err := writeEntry("signature", []byte(base64.StdEncoding.EncodeToString(sig)), 0o444); err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		data := files[rel]
		if err := writeEntry(rel, data, 0o444); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	return m.Digest, gz.Close()
}

func PublicKeyFromBase64(s string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ECDSA public key")
	}
	return pub, nil
}

func PublicKeyToBase64(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func PrivateKeyFromPEMFile(path string) (*ecdsa.PrivateKey, error) {
	data, err := safeio.ReadRegularFile(path, 64<<10, 0o077)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func VerifyAndMount(archive io.Reader, expectedDigest string, pub *ecdsa.PublicKey, destDir string) (*Pack, error) {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	var manifestBytes, sigBytes []byte
	files := map[string][]byte{}
	totalSize := int64(0)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported archive entry type for %s", hdr.Name)
		}
		name := filepath.ToSlash(hdr.Name)
		if !validPackPath(name) {
			return nil, fmt.Errorf("unsafe path in archive: %s", name)
		}
		if hdr.Size < 0 || hdr.Size > maxPackFileSize {
			return nil, fmt.Errorf("pack file %s exceeds size limit", name)
		}
		totalSize += hdr.Size
		if totalSize > maxPackTotalSize {
			return nil, fmt.Errorf("agent pack exceeds expanded size limit")
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxPackFileSize+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxPackFileSize {
			return nil, fmt.Errorf("pack file %s exceeds size limit", name)
		}
		switch name {
		case "pack.yaml":
			if manifestBytes != nil {
				return nil, fmt.Errorf("duplicate pack.yaml")
			}
			manifestBytes = data
		case "signature":
			if sigBytes != nil {
				return nil, fmt.Errorf("duplicate signature")
			}
			sigBytes = data
		default:
			if len(files) >= maxPackFiles {
				return nil, fmt.Errorf("agent pack exceeds file-count limit")
			}
			if _, exists := files[name]; exists {
				return nil, fmt.Errorf("duplicate pack file %s", name)
			}
			files[name] = data
		}
	}
	if manifestBytes == nil || sigBytes == nil {
		return nil, fmt.Errorf("archive missing pack.yaml or signature")
	}
	var m Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("pack manifest contains multiple YAML documents")
	}
	if m.Version == "" || len(m.Version) > 128 || (m.Provider != "" && m.Provider != "codex" && m.Provider != "claude" && m.Provider != "opencode") {
		return nil, fmt.Errorf("pack manifest has invalid version or provider")
	}
	if len(m.Files) == 0 || len(m.Files) > maxPackFiles {
		return nil, fmt.Errorf("pack manifest has invalid file count")
	}
	got := m.Digest
	if !validSHA256(got) || (expectedDigest != "" && !validSHA256(expectedDigest)) {
		return nil, fmt.Errorf("pack manifest has invalid digest")
	}
	m.Digest = ""
	unsignedBytes, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	contentSum := sha256.Sum256(unsignedBytes)
	digest := hex.EncodeToString(contentSum[:])
	if got != digest {
		return nil, fmt.Errorf("manifest digest invalid: got %s computed %s", got, digest)
	}
	if expectedDigest != "" && digest != expectedDigest {
		return nil, fmt.Errorf("digest mismatch: got %s want %s", digest, expectedDigest)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigBytes)))
	if err != nil {
		return nil, err
	}
	signSum := sha256.Sum256(manifestBytes)
	if !ecdsa.VerifyASN1(pub, signSum[:], sig) {
		return nil, fmt.Errorf("signature verification failed")
	}
	if len(files) != len(m.Files) {
		return nil, fmt.Errorf("file count mismatch: archive %d manifest %d", len(files), len(m.Files))
	}
	for rel, wantDigest := range m.Files {
		if !validPackPath(rel) || rel == "pack.yaml" || rel == "signature" {
			return nil, fmt.Errorf("unsafe manifest file path %s", rel)
		}
		if !validSHA256(wantDigest) {
			return nil, fmt.Errorf("file %s has invalid digest", rel)
		}
		data, ok := files[rel]
		if !ok {
			return nil, fmt.Errorf("manifest file %s missing from archive", rel)
		}
		fsum := sha256.Sum256(data)
		if hex.EncodeToString(fsum[:]) != wantDigest {
			return nil, fmt.Errorf("file %s digest mismatch", rel)
		}
	}
	if err := prepareDestination(destDir); err != nil {
		return nil, fmt.Errorf("prepare agent pack destination: %w", err)
	}
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dirSet := map[string]bool{".": true}
	for rel, data := range files {
		name := filepath.FromSlash(rel)
		dir := filepath.Dir(name)
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		for current := dir; current != "."; current = filepath.Dir(current) {
			dirSet[current] = true
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := root.Chmod(dir, 0o555); err != nil {
			return nil, err
		}
	}
	return &Pack{Manifest: m, Root: destDir}, nil
}

func prepareDestination(destDir string) error {
	info, err := os.Lstat(destDir)
	if os.IsNotExist(err) {
		return os.Mkdir(destDir, 0o700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination must be a real directory")
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("destination must be empty")
	}
	// #nosec G302 -- pack staging is a private directory; owner traversal requires 0700 before extraction.
	return os.Chmod(destDir, 0o700)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validPackPath(name string) bool {
	return name != "" && fs.ValidPath(name) && path.Clean(name) == name && !path.IsAbs(name)
}
