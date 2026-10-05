package remoteconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/transport"
)

const FetchTimeout = 10 * time.Second

var ErrNotEnrolled = errors.New("this runner is not enrolled; run our-runner enroll first")

var ErrNotModified = errors.New("the runner configuration has not changed")

type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return identityRefusedMessage
	case e.Code == "configuration_not_ready":
		return "the dashboard has not saved this runner's configuration yet"
	case e.Code == "direct_isolation_unavailable":
		return "the dashboard stored direct isolation, which this control plane only allows in development; choose Docker isolation in the dashboard"
	case e.Code == "configuration_conflict":
		return "two assigned projects deliver different Agent Pack keys with the same key ID; fix the project configuration in the dashboard"
	case e.Code == "configuration_too_large":
		return "this runner's configuration is too large; assign fewer projects or repositories"
	case e.Code == "configuration_changed":
		return "the dashboard changed this runner's configuration while it was being fetched; try again"
	case e.Status == http.StatusTooManyRequests:
		return "too many configuration requests from this runner; wait a minute and try again"
	case e.Code == "unavailable":
		return "the runner gateway is temporarily unavailable"
	default:
		return fmt.Sprintf("the runner gateway answered HTTP %d", e.Status)
	}
}

const identityRefusedMessage = "the gateway did not accept this runner's identity; the runner certificate is unknown, superseded or revoked, or the runner is not active"

type IdentityRefusedError struct {
	Err error
}

func (e *IdentityRefusedError) Error() string {
	return fmt.Sprintf("%s (%v)", identityRefusedMessage, e.Err)
}

func (e *IdentityRefusedError) Unwrap() error { return e.Err }

type NetworkError struct {
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("could not reach the runner gateway: %v", e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

func IsTransient(err error) bool {
	var networkErr *NetworkError
	if errors.As(err, &networkErr) {
		return true
	}
	var statusErr *StatusError
	return errors.As(err, &statusErr) && statusErr.Status >= 500 && statusErr.Status <= 599
}

func IsStatus(err error, status int, code string) bool {
	var statusErr *StatusError
	return errors.As(err, &statusErr) && statusErr.Status == status && statusErr.Code == code
}

type Fetcher struct {
	endpoint  string
	client    *http.Client
	notBefore time.Time
	notAfter  time.Time
}

func NewFetcher(stateDir, gatewayURL string) (*Fetcher, error) {
	endpoint, err := ConfigurationURL(gatewayURL)
	if err != nil {
		return nil, err
	}
	store := identity.NewStore(stateDir)
	if !store.Enrolled() {
		return nil, ErrNotEnrolled
	}
	certPEM, caPEM, err := store.LoadCert()
	if err != nil {
		return nil, fmt.Errorf("load runner certificate: %w", err)
	}
	notBefore, notAfter, err := store.CertExpiry()
	if err != nil {
		return nil, fmt.Errorf("load runner certificate: %w", err)
	}
	key, err := store.LoadKey()
	if err != nil {
		return nil, fmt.Errorf("load runner key: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("load runner key: %w", err)
	}
	tlsConfig, err := transport.TLSConfigFromPEM(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), caPEM)
	if err != nil {
		return nil, err
	}
	tlsConfig.MaxVersion = tls.VersionTLS13
	return &Fetcher{
		endpoint: endpoint,
		client: &http.Client{
			Timeout:       FetchTimeout,
			Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		notBefore: notBefore,
		notAfter:  notAfter,
	}, nil
}

func (f *Fetcher) Fetch(ctx context.Context) ([]byte, error) {
	return f.fetch(ctx, "")
}

func (f *Fetcher) FetchIfChanged(ctx context.Context, runningDigest string) ([]byte, error) {
	return f.fetch(ctx, runningDigest)
}

func (f *Fetcher) fetch(ctx context.Context, runningDigest string) ([]byte, error) {
	if now := time.Now(); now.Before(f.notBefore) {
		return nil, &IdentityRefusedError{Err: fmt.Errorf("the runner certificate is not valid until %s", f.notBefore.UTC().Format(time.RFC3339))}
	} else if now.After(f.notAfter) {
		return nil, &IdentityRefusedError{Err: fmt.Errorf("the runner certificate expired at %s", f.notAfter.UTC().Format(time.RFC3339))}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	conditional := digestPattern.MatchString(runningDigest)
	if conditional {
		request.Header.Set("If-None-Match", `"`+runningDigest+`"`)
	}
	response, err := f.client.Do(request)
	if err != nil {
		if certificateFailure(err) {
			return nil, fmt.Errorf("the runner gateway's TLS certificate is not trusted by this runner: %w", err)
		}
		if remoteTLSAlert(err) {
			return nil, &IdentityRefusedError{Err: err}
		}
		return nil, &NetworkError{Err: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified && conditional {
		return nil, ErrNotModified
	}
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &body)
		return nil, &StatusError{Status: response.StatusCode, Code: body.Code}
	}
	if response.ContentLength > MaxDeliveryBytes {
		return nil, fmt.Errorf("remote configuration exceeds %d bytes", MaxDeliveryBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxDeliveryBytes+1))
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	if len(raw) > MaxDeliveryBytes {
		return nil, fmt.Errorf("remote configuration exceeds %d bytes", MaxDeliveryBytes)
	}
	return raw, nil
}

func remoteTLSAlert(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "remote error"
}

func certificateFailure(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid)
}
