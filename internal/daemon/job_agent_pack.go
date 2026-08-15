package daemon

import (
	"context"
	"errors"
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

type agentPackMaterializationError struct {
	category  v1.AgentPackMaterializationFailureCategory
	retryable bool
	cause     error
}

func (failure *agentPackMaterializationError) Error() string {
	return "Agent Pack materialization failed"
}

func (failure *agentPackMaterializationError) Unwrap() error { return failure.cause }

func newAgentPackMaterializationError(
	category v1.AgentPackMaterializationFailureCategory,
	retryable bool,
	cause error,
) error {
	return &agentPackMaterializationError{
		category: category, retryable: retryable, cause: cause,
	}
}

func agentPackMaterializationEvidence(
	err error,
) (v1.AgentPackMaterializationFailureCategory, bool, bool) {
	var failure *agentPackMaterializationError
	if !errors.As(err, &failure) {
		return "", false, false
	}
	return failure.category, failure.retryable, true
}

func (d *Daemon) verifyJobAgentPackAuthority(job v1.JobSpec) error {
	if job.AgentPack == nil && job.AgentPackAuthority == nil {
		return nil
	}
	if job.AgentPack == nil || job.AgentPackAuthority == nil {
		return rejectCommand(v1.CommandAckReasonAgentPackAuthorityInvalid, nil)
	}
	keyID := job.AgentPack.KeyID
	trustedKey, ok := d.cfg.AgentPackPublicKeys[keyID]
	if !ok || trustedKey == "" {
		return rejectCommand(v1.CommandAckReasonAgentPackKeyUntrusted, nil)
	}
	if err := agentpack.VerifyDeliveryAuthority(
		*job.AgentPackAuthority,
		*job.AgentPack,
		trustedKey,
		d.cfg.Provider,
		Version,
	); err != nil {
		kind, _ := agentpack.DeliveryAuthorityFailureKindOf(err)
		if kind == agentpack.DeliveryAuthorityIncompatible {
			return rejectCommand(v1.CommandAckReasonAgentPackIncompatible, err)
		}
		return rejectCommand(v1.CommandAckReasonAgentPackAuthorityInvalid, err)
	}
	if err := d.validateArtifactURL(job.AgentPack.URL); err != nil {
		return rejectCommand(v1.CommandAckReasonAgentPackOriginUnapproved, err)
	}
	return nil
}

func (jr *jobRunner) materializeAgentPack(reference *v1.AgentPackRef, destination string) error {
	if reference == nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, nil)
	}
	if err := jr.d.validateArtifactURL(reference.URL); err != nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err)
	}
	cache := agentpack.ArchiveCache{
		Root:            filepath.Join(jr.d.cfg.StateDir, "agent-pack-cache", "archives"),
		MaxArchiveBytes: agentpack.DefaultMaxArchiveBytes,
	}
	archivePath, err := cache.Acquire(jr.ctx, reference.Digest, func(ctx context.Context, writer io.Writer) error {
		return jr.d.downloadAgentPack(ctx, reference.URL, writer)
	})
	if err != nil {
		if _, _, classified := agentPackMaterializationEvidence(err); classified {
			return err
		}
		switch {
		case errors.Is(err, agentpack.ErrArchiveDigestMismatch):
			return newAgentPackMaterializationError(v1.AgentPackFailureDigestMismatch, false, err)
		case errors.Is(err, agentpack.ErrArchiveInvalid), errors.Is(err, agentpack.ErrArchiveSizeLimit):
			return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err)
		default:
			return newAgentPackMaterializationError(v1.AgentPackFailureStorageUnavailable, true, err)
		}
	}
	archiveName := reference.Digest + ".tgz"
	if archivePath != filepath.Join(cache.Root, archiveName) {
		return newAgentPackMaterializationError(v1.AgentPackFailureStorageUnavailable, true, nil)
	}
	cacheRoot, err := os.OpenRoot(cache.Root)
	if err != nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureStorageUnavailable, true, err)
	}
	defer func() { _ = cacheRoot.Close() }()
	archive, err := cacheRoot.Open(archiveName)
	if err != nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureStorageUnavailable, true, err)
	}
	defer func() { _ = archive.Close() }()
	_, err = agentpack.MountAuthenticatedArchiveWithPolicy(
		&contextReader{ctx: jr.ctx, reader: archive},
		destination,
		agentpack.MountPolicy{Version: reference.Version, Provider: jr.d.cfg.Provider},
	)
	if err != nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err)
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
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	client := d.hardenedArtifactClient(rawURL)
	response, err := client.Do(request)
	if err != nil {
		return newAgentPackMaterializationError(v1.AgentPackFailureDownloadUnavailable, true, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooEarly ||
			response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			return newAgentPackMaterializationError(v1.AgentPackFailureDownloadUnavailable, true, nil)
		}
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, nil)
	}
	if response.ContentLength > agentpack.DefaultMaxArchiveBytes {
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, agentpack.ErrArchiveSizeLimit)
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, nil)
	}
	if _, err := io.Copy(destination, response.Body); err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return newAgentPackMaterializationError(v1.AgentPackFailureStorageUnavailable, true, err)
		}
		if errors.Is(err, agentpack.ErrArchiveSizeLimit) {
			return newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err)
		}
		return newAgentPackMaterializationError(v1.AgentPackFailureDownloadUnavailable, true, err)
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
