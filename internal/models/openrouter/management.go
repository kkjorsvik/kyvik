package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ManagementClient interacts with the OpenRouter Management API
// for provisioning and revoking per-agent API keys.
type ManagementClient struct {
	provisioningKey string
	baseURL         string
	httpClient      *http.Client
}

// ManagementOption configures a ManagementClient.
type ManagementOption func(*ManagementClient)

// WithManagementBaseURL overrides the default OpenRouter base URL for management calls.
func WithManagementBaseURL(url string) ManagementOption {
	return func(mc *ManagementClient) { mc.baseURL = url }
}

// WithManagementHTTPClient sets a custom HTTP client for management calls.
func WithManagementHTTPClient(hc *http.Client) ManagementOption {
	return func(mc *ManagementClient) { mc.httpClient = hc }
}

// NewManagementClient creates a new management API client.
func NewManagementClient(provisioningKey string, opts ...ManagementOption) *ManagementClient {
	mc := &ManagementClient{
		provisioningKey: provisioningKey,
		baseURL:         defaultBaseURL,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(mc)
	}
	return mc
}

// CreateKeyRequest describes a new key to provision.
type CreateKeyRequest struct {
	Name  string  `json:"name"`
	Limit float64 `json:"limit,omitempty"`
	Label string  `json:"label,omitempty"`
}

// CreateKeyResponse contains the result of key creation.
type CreateKeyResponse struct {
	Key  string `json:"key"`
	Hash string `json:"hash"`
	Name string `json:"name"`
}

// KeyInfo describes an existing provisioned key.
type KeyInfo struct {
	Hash      string    `json:"hash"`
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	Limit     float64   `json:"limit"`
	Usage     float64   `json:"usage"`
	CreatedAt time.Time `json:"created_at"`
	IsActive  bool      `json:"is_active"`
}

// CreateKey provisions a new API key via the management API.
func (mc *ManagementClient) CreateKey(ctx context.Context, req CreateKeyRequest) (*CreateKeyResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, mc.baseURL+"/api/v1/keys", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openrouter management: build request: %w", err)
	}
	mc.setHeaders(httpReq)

	resp, err := mc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: create key: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter management: create key failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data CreateKeyResponse `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("openrouter management: unmarshal response: %w", err)
	}

	return &result.Data, nil
}

// DeleteKey revokes a provisioned key by its hash.
func (mc *ManagementClient) DeleteKey(ctx context.Context, keyHash string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, mc.baseURL+"/api/v1/keys/"+keyHash, nil)
	if err != nil {
		return fmt.Errorf("openrouter management: build request: %w", err)
	}
	mc.setHeaders(httpReq)

	resp, err := mc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openrouter management: delete key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openrouter management: delete key failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ListKeys returns all provisioned keys.
func (mc *ManagementClient) ListKeys(ctx context.Context) ([]KeyInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mc.baseURL+"/api/v1/keys", nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: build request: %w", err)
	}
	mc.setHeaders(httpReq)

	resp, err := mc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: list keys: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter management: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter management: list keys failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []KeyInfo `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("openrouter management: unmarshal response: %w", err)
	}

	return result.Data, nil
}

func (mc *ManagementClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+mc.provisioningKey)
	req.Header.Set("Content-Type", "application/json")
}
