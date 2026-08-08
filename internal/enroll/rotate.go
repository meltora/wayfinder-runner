package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type RotateRequest struct {
	CSR           string `json:"csr"`
	RunnerVersion string `json:"runner_version"`
}

func (c *Client) Rotate(ctx context.Context, req RotateRequest) (*Response, error) {
	if err := c.validateBaseURL(); err != nil {
		return nil, err
	}
	if len(req.CSR) == 0 || len(req.CSR) > 1<<20 || len(req.RunnerVersion) > 128 {
		return nil, fmt.Errorf("rotation request fields are missing or exceed size limits")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpClient := c.secureHTTPClient()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+"/v1/runners/rotate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rotate request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > 1<<20 {
		return nil, fmt.Errorf("rotate response exceeds size limit")
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(respBody) > 1<<20 {
		return nil, fmt.Errorf("rotate response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rotate rejected: status %d: %s", resp.StatusCode, truncateResponse(respBody))
	}
	var out Response
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse rotate response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("parse rotate response: trailing JSON")
	}
	if out.RunnerID == "" || out.Certificate == "" || out.CACert == "" {
		return nil, fmt.Errorf("rotate response missing fields")
	}
	return &out, nil
}
