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

type discoveryDocument struct {
	SchemaVersion int    `json:"schema_version"`
	GatewayURL    string `json:"gateway_url"`
}

func (c *Client) DiscoverGateway(ctx context.Context) (string, error) {
	if err := c.validateBaseURL(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.BaseURL, "/")+"/v1/runners/discovery", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.secureHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway discovery request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > 1<<20 {
		return "", fmt.Errorf("gateway discovery response exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(body) > 1<<20 {
		return "", fmt.Errorf("gateway discovery response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway discovery unavailable: status %d", resp.StatusCode)
	}
	var out discoveryDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return "", fmt.Errorf("parse gateway discovery response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return "", fmt.Errorf("parse gateway discovery response: trailing JSON")
	}
	if out.SchemaVersion != 1 || out.GatewayURL == "" {
		return "", fmt.Errorf("gateway discovery response has an unsupported schema")
	}
	return out.GatewayURL, nil
}
