package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth2TokenResult represents a token response from an OAuth2 provider.
type OAuth2TokenResult struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ObtainedAt   time.Time `json:"obtained_at"`
}

// IsExpired returns true if the token has expired (with 60s buffer).
func (t *OAuth2TokenResult) IsExpired() bool {
	if t.ExpiresIn <= 0 {
		return true
	}
	return time.Since(t.ObtainedAt) > time.Duration(t.ExpiresIn-60)*time.Second
}

// OAuth2Manager handles OAuth2 token refresh for the REST API tool.
type OAuth2Manager struct {
	secretResolver SecretResolverFunc
	secretWriter   OAuth2SecretWriter
	mu             sync.Mutex
	// In-memory cache of active access tokens (agentID:ref → token).
	tokens map[string]*OAuth2TokenResult
}

// OAuth2SecretWriter writes updated secrets back to the vault.
type OAuth2SecretWriter func(ctx context.Context, agentID, key, value string) error

// NewOAuth2Manager creates a new OAuth2 manager.
func NewOAuth2Manager(resolver SecretResolverFunc, writer OAuth2SecretWriter) *OAuth2Manager {
	return &OAuth2Manager{
		secretResolver: resolver,
		secretWriter:   writer,
		tokens:         make(map[string]*OAuth2TokenResult),
	}
}

// GetAccessToken retrieves a valid access token for the given endpoint auth config.
// It refreshes the token if it has expired.
func (m *OAuth2Manager) GetAccessToken(ctx context.Context, agentID, teamID string, auth OAuth2AuthConfig) (string, error) {
	cacheKey := agentID + ":" + auth.AccessTokenRef

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check in-memory cache first.
	if tok, ok := m.tokens[cacheKey]; ok && !tok.IsExpired() {
		return tok.AccessToken, nil
	}

	// Try to load cached access token from vault.
	if auth.AccessTokenRef != "" {
		if cached, err := m.secretResolver(ctx, agentID, teamID, auth.AccessTokenRef); err == nil && cached != "" {
			var tok OAuth2TokenResult
			if err := json.Unmarshal([]byte(cached), &tok); err == nil && !tok.IsExpired() {
				m.tokens[cacheKey] = &tok
				return tok.AccessToken, nil
			}
		}
	}

	// Token expired or not found — refresh it.
	refreshToken, err := m.secretResolver(ctx, agentID, teamID, auth.RefreshTokenRef)
	if err != nil {
		return "", fmt.Errorf("oauth2: refresh token %q not found: %w", auth.RefreshTokenRef, err)
	}

	clientID, err := m.secretResolver(ctx, agentID, teamID, auth.ClientIDRef)
	if err != nil {
		return "", fmt.Errorf("oauth2: client_id %q not found: %w", auth.ClientIDRef, err)
	}

	clientSecret, err := m.secretResolver(ctx, agentID, teamID, auth.ClientSecretRef)
	if err != nil {
		return "", fmt.Errorf("oauth2: client_secret %q not found: %w", auth.ClientSecretRef, err)
	}

	tok, err := refreshAccessToken(ctx, auth.TokenURL, clientID, clientSecret, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth2: token refresh failed: %w", err)
	}

	// Update refresh token if the provider returned a new one.
	if tok.RefreshToken != "" && tok.RefreshToken != refreshToken {
		if m.secretWriter != nil {
			_ = m.secretWriter(ctx, agentID, auth.RefreshTokenRef, tok.RefreshToken)
		}
	}

	// Cache the access token in vault.
	if auth.AccessTokenRef != "" && m.secretWriter != nil {
		if data, err := json.Marshal(tok); err == nil {
			_ = m.secretWriter(ctx, agentID, auth.AccessTokenRef, string(data))
		}
	}

	m.tokens[cacheKey] = tok
	return tok.AccessToken, nil
}

// OAuth2AuthConfig extracts the OAuth2-related fields from a RESTAPIAuth for token operations.
type OAuth2AuthConfig struct {
	ClientIDRef     string
	ClientSecretRef string
	TokenURL        string
	RefreshTokenRef string
	AccessTokenRef  string
	Scopes          string
}

// refreshAccessToken performs the OAuth2 token refresh grant.
func refreshAccessToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*OAuth2TokenResult, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result OAuth2TokenResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	result.ObtainedAt = time.Now()
	return &result, nil
}
