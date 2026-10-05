package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL       string
	HTTPClient    *http.Client
	AllowInsecure bool
}

type Request struct {
	EnrollmentCode string `json:"enrollment_code"`
	CSR            string `json:"csr"`
	RunnerVersion  string `json:"runner_version"`
}

type Response struct {
	RunnerID    string `json:"runner_id"`
	Certificate string `json:"certificate"`
	CACert      string `json:"ca_certificate"`
}

func (c *Client) Enroll(ctx context.Context, req Request) (*Response, error) {
	if err := c.validateBaseURL(); err != nil {
		return nil, err
	}
	if req.EnrollmentCode == "" || len(req.EnrollmentCode) > 4096 || len(req.CSR) == 0 || len(req.CSR) > 1<<20 || len(req.RunnerVersion) > 128 {
		return nil, fmt.Errorf("enrollment request fields are missing or exceed size limits")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpClient := c.secureHTTPClient()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+"/v1/runners/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("enroll request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > 1<<20 {
		return nil, fmt.Errorf("enroll response exceeds size limit")
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(respBody) > 1<<20 {
		return nil, fmt.Errorf("enroll response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newRejectedError(resp, respBody)
	}
	var out Response
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse enroll response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("parse enroll response: trailing JSON")
	}
	if out.RunnerID == "" || out.Certificate == "" || out.CACert == "" {
		return nil, fmt.Errorf("enroll response missing fields")
	}
	return &out, nil
}

func (c *Client) validateBaseURL() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid control-plane URL")
	}
	if u.Scheme != "https" && (!c.AllowInsecure || u.Scheme != "http") {
		return fmt.Errorf("control-plane enrollment and rotation require HTTPS")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("control-plane URL must not contain credentials, query parameters, or fragments")
	}
	return nil
}

func (c *Client) secureHTTPClient() *http.Client {
	base := c.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("control-plane redirects are disabled")
	}
	return &client
}

func truncateResponse(raw []byte) string {
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	return string(raw)
}
