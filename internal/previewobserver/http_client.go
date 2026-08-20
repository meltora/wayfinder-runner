package previewobserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxResponseHeaderBytes = 32 << 10

func (registry *Registry) executeCheck(
	ctx context.Context,
	compiled profile,
	target resolvedTarget,
	checkID string,
) (v1.PreviewObservationCheck, error) {
	check, exists := compiled.healthChecks[checkID]
	if !exists {
		return v1.PreviewObservationCheck{}, resultError(v1.PreviewProfileUnavailable)
	}
	checkCtx, cancel := context.WithTimeout(ctx, check.timeout)
	defer cancel()
	requestURL := (&url.URL{Scheme: "https", Host: target.hostname, Path: check.path}).String()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return v1.PreviewObservationCheck{}, resultError(v1.PreviewInvalidObserverResponse)
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("User-Agent", "our-runner-preview-observer/1")
	if compiled.authorization != "" {
		request.Header.Set("Authorization", compiled.authorization)
	}
	transport := registry.transportFor(compiled, target, check.timeout)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	startedAt := registry.dependencies.Now()
	response, err := client.Do(request)
	if err != nil {
		return v1.PreviewObservationCheck{}, classifyRequestError(err)
	}
	defer func() { _ = response.Body.Close() }()
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, compiled.maxResponseBytes+1))
	if err != nil {
		return v1.PreviewObservationCheck{}, resultError(v1.PreviewUnreachable)
	}
	if read > compiled.maxResponseBytes {
		return v1.PreviewObservationCheck{}, resultError(v1.PreviewInvalidObserverResponse)
	}
	if response.StatusCode < 100 || response.StatusCode > 599 {
		return v1.PreviewObservationCheck{}, resultError(v1.PreviewInvalidObserverResponse)
	}
	status := v1.PreviewObservationCheckFailed
	if _, expected := check.expectedStatuses[response.StatusCode]; expected {
		status = v1.PreviewObservationCheckPassed
	}
	elapsed := registry.dependencies.Now().Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	durationMS := uint64(elapsed / time.Millisecond)
	if durationMS > 120_000 {
		durationMS = 120_000
	}
	return v1.PreviewObservationCheck{
		CheckID: checkID, Status: status,
		HTTPStatus: response.StatusCode, DurationMS: durationMS,
	}, nil
}

func (registry *Registry) transportFor(
	compiled profile,
	target resolvedTarget,
	timeout time.Duration,
) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		ForceAttemptHTTP2:      false,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxConnsPerHost:        1,
		ResponseHeaderTimeout:  timeout,
		TLSHandshakeTimeout:    min(timeout, 10*time.Second),
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: target.hostname,
			RootCAs:    compiled.rootCAs,
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				return nil, resultError(v1.PreviewTargetNotAllowed)
			}
			return registry.dependencies.DialContext(
				ctx,
				"tcp",
				net.JoinHostPort(target.address.String(), "443"),
			)
		},
	}
}

func classifyRequestError(err error) error {
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &verification) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &recordHeader) {
		return resultError(v1.PreviewTLSVerificationFailed)
	}
	return resultError(v1.PreviewUnreachable)
}
