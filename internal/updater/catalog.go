package updater

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/example/app-runner/internal/updatecatalog"
)

const maxCatalogBytes = 256 << 10

type VerifiedCatalog struct {
	ManifestURL            string
	AllowedOrigins         []string
	Trust                  updatecatalog.TrustPolicy
	Client                 *http.Client
	Clock                  Clock
	ClockSkew, MaxLifetime time.Duration
}

func (v *VerifiedCatalog) Latest(ctx context.Context, q CatalogQuery) (Release, error) {
	if len(v.Trust.Keys) == 0 {
		return Release{}, fmt.Errorf("update trust is empty")
	}
	if _, err := validateHTTPSURL(v.ManifestURL, v.AllowedOrigins); err != nil {
		return Release{}, fmt.Errorf("manifest URL: %w", err)
	}
	client := strictHTTPClient(v.Client, v.AllowedOrigins, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.ManifestURL, nil)
	if err != nil {
		return Release{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxCatalogBytes {
		return Release{}, fmt.Errorf("manifest too large")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return Release{}, err
	}
	if len(raw) > maxCatalogBytes {
		return Release{}, fmt.Errorf("manifest too large")
	}
	now := time.Now()
	if v.Clock != nil {
		now = v.Clock.Now()
	}
	verified, err := updatecatalog.ParseAndVerify(raw, updatecatalog.VerifyPolicy{Channel: q.Channel, OS: q.OS, Arch: q.Arch, RunnerVersion: q.RunnerVersion, UpdaterVersion: q.UpdaterVersion, Now: now, ClockSkew: v.ClockSkew, MaxLifetime: v.MaxLifetime, Trust: v.Trust, Current: updatecatalog.State{Sequence: q.CurrentSequence, ReleaseID: q.CurrentReleaseID, PayloadSHA256: q.CurrentPayloadDigest}})
	if err != nil {
		return Release{}, err
	}
	a := verified.Artifact
	if a.Format != "tar.gz" {
		return Release{}, fmt.Errorf("unsupported artifact format %q", a.Format)
	}
	return Release{ReleaseID: verified.Manifest.ReleaseID, Version: verified.Manifest.Version, Sequence: verified.Manifest.Sequence, Channel: verified.Manifest.Channel, OS: a.OS, Arch: a.Arch, URL: a.URL, Size: a.Size, SHA256: a.SHA256, PayloadDigest: verified.PayloadSHA256}, nil
}

type HTTPArtifactFetcher struct {
	AllowedOrigins []string
	Client         *http.Client
	MaxSize        int64
}

func (f *HTTPArtifactFetcher) Download(ctx context.Context, rawURL string, size int64, digest string, dst io.Writer) error {
	if size <= 0 || size > f.MaxSize {
		return fmt.Errorf("artifact size exceeds policy")
	}
	if _, err := validateHTTPSURL(rawURL, f.AllowedOrigins); err != nil {
		return err
	}
	client := strictHTTPClient(f.Client, f.AllowedOrigins, 10*time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact status %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return fmt.Errorf("artifact content length mismatch")
	}
	h := newSHA256Writer(dst)
	n, err := io.Copy(h, io.LimitReader(resp.Body, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("artifact exact size mismatch")
	}
	if h.Sum() != digest {
		return fmt.Errorf("artifact digest mismatch")
	}
	return nil
}

type digestWriter struct {
	dst  io.Writer
	hash hash.Hash
}

func newSHA256Writer(dst io.Writer) *digestWriter { return &digestWriter{dst: dst, hash: sha256.New()} }
func (d *digestWriter) Write(p []byte) (int, error) {
	n, err := d.dst.Write(p)
	if n > 0 {
		_, _ = d.hash.Write(p[:n])
	}
	return n, err
}
func (d *digestWriter) Sum() string { return fmt.Sprintf("%x", d.hash.Sum(nil)) }

func strictHTTPClient(base *http.Client, origins []string, timeout time.Duration) *http.Client {
	var c http.Client
	if base != nil {
		c = *base
	}
	if c.Timeout <= 0 || c.Timeout > timeout {
		c.Timeout = timeout
	}
	if c.Transport == nil {
		c.Transport = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true}
	}
	prior := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if _, err := validateHTTPSURL(req.URL.String(), origins); err != nil {
			return err
		}
		if prior != nil {
			return prior(req, via)
		}
		return nil
	}
	return &c
}
func validateHTTPSURL(raw string, origins []string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("must be an absolute credential-free HTTPS URL")
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, allowed := range origins {
		a, err := url.Parse(allowed)
		if err == nil && a.Path == "" && a.RawQuery == "" && a.Fragment == "" && strings.ToLower(a.Scheme+"://"+a.Host) == origin {
			return u, nil
		}
	}
	return nil, fmt.Errorf("origin is not allowed")
}

type trustDocument struct {
	SchemaVersion      int        `json:"schema_version"`
	SignatureThreshold int        `json:"signature_threshold"`
	Keys               []trustKey `json:"keys"`
}
type trustKey struct {
	KeyID        string     `json:"key_id"`
	PublicKeyB64 string     `json:"public_key_b64"`
	NotBefore    time.Time  `json:"not_before"`
	NotAfter     time.Time  `json:"not_after"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

func LoadTrustFile(path string, requireRoot bool) (updatecatalog.TrustPolicy, error) {
	if !filepath.IsAbs(path) {
		return updatecatalog.TrustPolicy{}, fmt.Errorf("trust file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return updatecatalog.TrustPolicy{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o137 != 0 || info.Size() <= 0 || info.Size() > 256<<10 {
		return updatecatalog.TrustPolicy{}, fmt.Errorf("trust file is not a private bounded regular file")
	}
	if requireRoot && !ownedByRoot(info) {
		return updatecatalog.TrustPolicy{}, fmt.Errorf("trust file must be owned by root")
	}
	// #nosec G304 -- the absolute trust path was lstat-checked as a private,
	// root-owned, bounded regular file immediately above.
	raw, err := os.ReadFile(path)
	if err != nil {
		return updatecatalog.TrustPolicy{}, err
	}
	if err := rejectDuplicateJSON(raw); err != nil {
		return updatecatalog.TrustPolicy{}, err
	}
	var doc trustDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return updatecatalog.TrustPolicy{}, err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return updatecatalog.TrustPolicy{}, fmt.Errorf("trust file has trailing data")
	}
	if doc.SchemaVersion != 1 || doc.SignatureThreshold < 1 || doc.SignatureThreshold > len(doc.Keys) {
		return updatecatalog.TrustPolicy{}, fmt.Errorf("invalid trust policy")
	}
	seen := map[string]bool{}
	keys := make([]updatecatalog.TrustedKey, 0, len(doc.Keys))
	for _, k := range doc.Keys {
		if !safeComponent(k.KeyID) || seen[k.KeyID] {
			return updatecatalog.TrustPolicy{}, fmt.Errorf("invalid or duplicate key id")
		}
		seen[k.KeyID] = true
		der, err := base64.StdEncoding.Strict().DecodeString(k.PublicKeyB64)
		if err != nil {
			return updatecatalog.TrustPolicy{}, err
		}
		parsed, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return updatecatalog.TrustPolicy{}, err
		}
		pub, ok := parsed.(*ecdsa.PublicKey)
		if !ok {
			return updatecatalog.TrustPolicy{}, fmt.Errorf("trusted key must be ECDSA")
		}
		keys = append(keys, updatecatalog.TrustedKey{KeyID: k.KeyID, PublicKey: pub, NotBefore: k.NotBefore, NotAfter: k.NotAfter, RevokedAt: k.RevokedAt})
	}
	return updatecatalog.TrustPolicy{Threshold: doc.SignatureThreshold, Keys: keys}, nil
}
func ownedByRoot(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

func validateRootDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return fmt.Errorf("%s must be a root-owned directory", path)
	}
	mask := os.FileMode(0o022)
	if private {
		mask = 0o077
	}
	if info.Mode().Perm()&mask != 0 {
		return fmt.Errorf("%s has unsafe permissions", path)
	}
	return nil
}

func rejectDuplicateJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSON(dec); err != nil {
		return fmt.Errorf("invalid trust JSON: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trust JSON has trailing data")
	}
	return nil
}
func scanJSON(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate field %q", key)
			}
			seen[key] = true
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("invalid delimiter")
	}
}
