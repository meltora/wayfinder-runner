package previewlifecycle

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const maxResponseHeaderBytes = 32 << 10

type controllerRequest struct {
	ControllerProtocolVersion int                          `json:"controller_protocol_version"`
	Operation                 v1.PreviewLifecycleOperation `json:"operation"`
}

type controllerResponse struct {
	ControllerProtocolVersion int                              `json:"controller_protocol_version"`
	ControllerReceiptDigest   string                           `json:"controller_receipt_digest"`
	ResourceBindingDigest     string                           `json:"resource_binding_digest"`
	ResourceState             v1.PreviewLifecycleResourceState `json:"resource_state"`
}

func (registry *Registry) apply(
	ctx context.Context,
	compiled controller,
	operation v1.PreviewLifecycleOperation,
) v1.PreviewLifecycleResultOutcome {
	raw, err := json.Marshal(controllerRequest{
		ControllerProtocolVersion: v1.PreviewLifecycleControllerProtocolVersion,
		Operation:                 operation,
	})
	if err != nil {
		return permanent(v1.PreviewLifecycleInvalidControllerResponse)
	}
	requestCtx, cancel := context.WithTimeout(ctx, compiled.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, compiled.controllerURL.String(), bytes.NewReader(raw))
	if err != nil {
		return permanent(v1.PreviewLifecycleProfileUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "our-runner-preview-lifecycle-controller/1")
	request.Header.Set("Idempotency-Key", operation.IdempotencyKey)
	request.Header.Set("X-Preview-Lifecycle-Fence", strconv.FormatUint(operation.FencingToken, 10))
	if compiled.authorization != "" {
		request.Header.Set("Authorization", compiled.authorization)
	}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: false, DisableCompression: true, DisableKeepAlives: true,
		MaxConnsPerHost: 1, ResponseHeaderTimeout: compiled.timeout,
		TLSHandshakeTimeout: min(compiled.timeout, 10*time.Second), MaxResponseHeaderBytes: maxResponseHeaderBytes,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: compiled.rootCAs},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: compiled.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) || isTimeout(err) {
			return uncertain(v1.PreviewLifecycleControllerTimeout)
		}
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, compiled.maxResponseBytes+1))
		return classifyControllerStatus(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	limited := io.LimitReader(response.Body, compiled.maxResponseBytes+1)
	responseRaw, err := io.ReadAll(limited)
	if err != nil {
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	if int64(len(responseRaw)) > compiled.maxResponseBytes {
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	var decoded controllerResponse
	decoder := json.NewDecoder(bytes.NewReader(responseRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		decoded.ControllerProtocolVersion != v1.PreviewLifecycleControllerProtocolVersion {
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	outcome := v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded, ControllerReceiptDigest: decoded.ControllerReceiptDigest,
		ResourceBindingDigest: decoded.ResourceBindingDigest, ResourceState: &decoded.ResourceState,
	}
	observedAt := registry.dependencies.Now().UTC()
	if observedAt.After(operation.LeaseExpiresAt) {
		return uncertain(v1.PreviewLifecycleControllerTimeout)
	}
	candidate := v1.PreviewLifecycleResultEvent{
		Kind:                            v1.EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(operation),
		CommandMessageID:                "preview-lifecycle-command-" + strings.Repeat("0", 64),
		ObservedAt:                      observedAt, Outcome: outcome,
	}
	if err := validation.PreviewLifecycleResultEvent(candidate); err != nil {
		return uncertain(v1.PreviewLifecycleReceiptUnavailable)
	}
	return outcome
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func classifyControllerStatus(response *http.Response) v1.PreviewLifecycleResultOutcome {
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return permanent(v1.PreviewLifecyclePolicyDenied)
	case http.StatusNotFound:
		return permanent(v1.PreviewLifecycleResourceNotFound)
	case http.StatusConflict:
		return retryable(v1.PreviewLifecycleTransientResourceConflict, nil)
	case http.StatusTooManyRequests:
		return retryable(v1.PreviewLifecycleRateLimited, retryAfter(response.Header.Get("Retry-After")))
	default:
		if response.StatusCode >= 500 && response.StatusCode <= 599 {
			return uncertain(v1.PreviewLifecycleReceiptUnavailable)
		}
		return permanent(v1.PreviewLifecycleInvalidControllerResponse)
	}
}

func retryAfter(value string) *uint64 {
	seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil || seconds > 24*60*60 {
		return nil
	}
	milliseconds := seconds * 1000
	return &milliseconds
}
