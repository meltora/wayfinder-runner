package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
)

const (
	agentPackFetchTimeout        = 2 * time.Minute
	agentPackResponseHeaderLimit = 1 << 20
)

type agentPackMaterializationError struct{ cause error }

func (failure *agentPackMaterializationError) Error() string {
	return "Agent Pack materialization failed: " + failure.cause.Error()
}

func (failure *agentPackMaterializationError) Unwrap() error { return failure.cause }

func (d *Daemon) verifyJobAgentPackAuthority(job v1.JobSpec) error {
	if job.AgentPack == nil && job.AgentPackAuthority == nil {
		return nil
	}
	if job.AgentPack == nil || job.AgentPackAuthority == nil {
		return fmt.Errorf("Agent Pack and signed authority must be provided together")
	}
	keyID := job.AgentPack.KeyID
	trustedKey, ok := d.cfg.AgentPackPublicKeys[keyID]
	if !ok || trustedKey == "" {
		return fmt.Errorf("Agent Pack signing key %q is not trusted by this runner", keyID)
	}
	if err := agentpack.VerifyDeliveryAuthority(
		*job.AgentPackAuthority,
		*job.AgentPack,
		trustedKey,
		d.cfg.Provider,
		Version,
	); err != nil {
		return fmt.Errorf("verify signed Agent Pack delivery authority: %w", err)
	}
	return nil
}

func (jr *jobRunner) materializeAgentPack(reference *v1.AgentPackRef, destination string) error {
	if reference == nil {
		return fmt.Errorf("Agent Pack reference is required")
	}
	if err := jr.d.validateArtifactURL(reference.URL); err != nil {
		return err
	}
	cache := agentpack.ArchiveCache{
		Root:            filepath.Join(jr.d.cfg.StateDir, "agent-pack-cache", "archives"),
		MaxArchiveBytes: agentpack.DefaultMaxArchiveBytes,
	}
	archivePath, err := cache.Acquire(jr.ctx, reference.Digest, func(ctx context.Context, writer io.Writer) error {
		return jr.d.downloadAgentPack(ctx, reference.URL, writer)
	})
	if err != nil {
		return err
	}
	archiveName := reference.Digest + ".tgz"
	if archivePath != filepath.Join(cache.Root, archiveName) {
		return fmt.Errorf("Agent Pack cache returned an unexpected path")
	}
	cacheRoot, err := os.OpenRoot(cache.Root)
	if err != nil {
		return fmt.Errorf("open Agent Pack cache root: %w", err)
	}
	defer func() { _ = cacheRoot.Close() }()
	archive, err := cacheRoot.Open(archiveName)
	if err != nil {
		return fmt.Errorf("open verified Agent Pack cache artifact: %w", err)
	}
	defer func() { _ = archive.Close() }()
	_, err = agentpack.MountAuthenticatedArchiveWithPolicy(
		&contextReader{ctx: jr.ctx, reader: archive},
		destination,
		agentpack.MountPolicy{Version: reference.Version, Provider: jr.d.cfg.Provider},
	)
	if err != nil {
		return err
	}
	if err := cache.Prune(agentpack.DefaultMaxCacheEntries, agentpack.DefaultMaxCacheBytes, reference.Digest); err != nil && jr.logger != nil {
		jr.logger.Warn("prune Agent Pack cache", "err", err)
	}
	return nil
}

func (d *Daemon) downloadAgentPack(ctx context.Context, rawURL string, destination io.Writer) error {
	requestCtx, cancel := context.WithTimeout(ctx, agentPackFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create artifact request")
	}
	request.Header.Set("Accept-Encoding", "identity")
	client := d.hardenedArtifactClient(rawURL)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("artifact request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact server returned status %d", response.StatusCode)
	}
	if response.ContentLength > agentpack.DefaultMaxArchiveBytes {
		return fmt.Errorf("Agent Pack archive exceeds compressed size limit")
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("Agent Pack response uses unsupported content encoding")
	}
	if _, err := io.Copy(destination, response.Body); err != nil {
		return fmt.Errorf("read Agent Pack response")
	}
	return nil
}

func (d *Daemon) hardenedArtifactClient(rawURL string) *http.Client {
	base := d.httpClientForURL(rawURL, agentPackFetchTimeout)
	client := *base
	client.Timeout = agentPackFetchTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transportSource := base.Transport
	if transportSource == nil {
		transportSource = http.DefaultTransport
	}
	if baseTransport, ok := transportSource.(*http.Transport); ok {
		transport := baseTransport.Clone()
		transport.DisableCompression = true
		transport.ResponseHeaderTimeout = 30 * time.Second
		transport.TLSHandshakeTimeout = 15 * time.Second
		transport.ExpectContinueTimeout = time.Second
		transport.MaxResponseHeaderBytes = agentPackResponseHeaderLimit
		if transport.DialContext == nil {
			transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
		client.Transport = transport
	}
	return &client
}

func (d *Daemon) validateArtifactURL(raw string) error {
	candidateOrigin, err := canonicalArtifactOrigin(raw, false)
	if err != nil {
		return err
	}
	origins := append([]string(nil), d.cfg.AllowedArtifactOrigins...)
	if len(origins) == 0 {
		controlPlaneHTTPURL := d.cfg.ControlPlaneHTTPURL
		if controlPlaneHTTPURL == "" {
			controlPlaneHTTPURL = deriveHTTPURL(d.cfg.ControlPlaneURL)
		}
		origins = append(origins, controlPlaneHTTPURL)
	}
	for _, rawOrigin := range origins {
		origin, originErr := canonicalArtifactOrigin(rawOrigin, false)
		if originErr == nil && origin == candidateOrigin {
			return nil
		}
	}
	return fmt.Errorf("artifact URL origin is not approved")
}

func canonicalArtifactOrigin(raw string, configuredOrigin bool) (string, error) {
	if !strings.HasPrefix(raw, "https://") {
		return "", fmt.Errorf("artifact URL must use HTTPS")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("artifact URL must be an absolute HTTPS URL without credentials or fragments")
	}
	if configuredOrigin && (parsed.RawQuery != "" || (parsed.Path != "" && parsed.Path != "/")) {
		return "", fmt.Errorf("configured artifact origin cannot contain a path or query")
	}
	if parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(parsed.Hostname(), ".") || parsed.Port() == "443" {
		return "", fmt.Errorf("artifact URL contains a non-canonical origin")
	}
	for _, value := range []byte(parsed.Host) {
		if value >= 0x80 {
			return "", fmt.Errorf("artifact URL origin must use canonical ASCII hostname encoding")
		}
	}
	return "https://" + parsed.Host, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
