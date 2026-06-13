package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	oauthStateCookie = "oauth_state"
	oauthStateTTL    = 10 * time.Minute
)

// OAuthStart initiates the OAuth2 authorization code flow for an integration.
// GET /oauth/start?integration={name}&agent_id={id}
func (h *Handlers) OAuthStart(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	integrationName := r.URL.Query().Get("integration")
	agentID := r.URL.Query().Get("agent_id")
	if integrationName == "" || agentID == "" {
		http.Error(w, "integration and agent_id are required", http.StatusBadRequest)
		return
	}

	tmpl, err := mgr.Get(integrationName)
	if err != nil {
		http.Error(w, "Integration not found", http.StatusNotFound)
		return
	}

	if tmpl.Auth.Type != "oauth2" {
		http.Error(w, "Integration does not use OAuth2", http.StatusBadRequest)
		return
	}

	if tmpl.Auth.AuthURL == "" || tmpl.Auth.TokenURL == "" {
		http.Error(w, "Integration OAuth2 config missing auth_url or token_url", http.StatusBadRequest)
		return
	}

	// Resolve client_id from vault.
	clientIDKey := fmt.Sprintf("integrations/%s/%s", integrationName, tmpl.Auth.ClientIDRef)
	clientID, err := h.secrets.Get(r.Context(), agentID, clientIDKey)
	if err != nil || clientID == "" {
		http.Error(w, "Client ID not found in vault. Please install the integration first.", http.StatusBadRequest)
		return
	}

	// Generate random state token.
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		http.Error(w, "failed to generate state token", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(stateBytes)

	// Store state in signed cookie.
	expiry := time.Now().Add(oauthStateTTL).Unix()
	cookieValue := h.signOAuthState(state, integrationName, agentID, expiry)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode, // Lax needed for OAuth redirect back
		MaxAge:   int(oauthStateTTL.Seconds()),
	})

	// Build authorization URL.
	authURL, err := url.Parse(tmpl.Auth.AuthURL)
	if err != nil {
		http.Error(w, "invalid auth_url in integration config", http.StatusInternalServerError)
		return
	}

	// Build redirect URI from the current request.
	scheme := "https"
	if !h.isSecureRequest(r) {
		scheme = "http"
	}
	redirectURI := fmt.Sprintf("%s://%s/oauth/callback", scheme, r.Host)

	q := authURL.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	if tmpl.Auth.Scopes != "" {
		q.Set("scope", tmpl.Auth.Scopes)
	}
	authURL.RawQuery = q.Encode()

	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

// OAuthCallback handles the OAuth2 provider redirect with authorization code.
// GET /oauth/callback?code={code}&state={state}
func (h *Handlers) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if r.URL.Query().Get("error") != "" {
		errDesc := r.URL.Query().Get("error_description")
		if errDesc == "" {
			errDesc = r.URL.Query().Get("error")
		}
		http.Error(w, "OAuth2 authorization denied: "+errDesc, http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "missing code or state parameter", http.StatusBadRequest)
		return
	}

	// Validate state cookie.
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil {
		http.Error(w, "missing OAuth state cookie (expired or blocked)", http.StatusBadRequest)
		return
	}

	integrationName, agentID, err := h.validateOAuthState(cookie.Value, state)
	if err != nil {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}

	tmpl, err := mgr.Get(integrationName)
	if err != nil {
		http.Error(w, "Integration not found", http.StatusNotFound)
		return
	}

	// Resolve client credentials from vault.
	clientIDKey := fmt.Sprintf("integrations/%s/%s", integrationName, tmpl.Auth.ClientIDRef)
	clientSecretKey := fmt.Sprintf("integrations/%s/%s", integrationName, tmpl.Auth.ClientSecretRef)

	clientID, err := h.secrets.Get(r.Context(), agentID, clientIDKey)
	if err != nil || clientID == "" {
		http.Error(w, "Client ID not found in vault", http.StatusInternalServerError)
		return
	}
	clientSecret, err := h.secrets.Get(r.Context(), agentID, clientSecretKey)
	if err != nil || clientSecret == "" {
		http.Error(w, "Client Secret not found in vault", http.StatusInternalServerError)
		return
	}

	// Build redirect URI matching what was sent in the start request.
	scheme := "https"
	if !h.isSecureRequest(r) {
		scheme = "http"
	}
	redirectURI := fmt.Sprintf("%s://%s/oauth/callback", scheme, r.Host)

	// Exchange authorization code for tokens.
	tokenResp, err := exchangeAuthCode(r.Context(), tmpl.Auth.TokenURL, clientID, clientSecret, code, redirectURI)
	if err != nil {
		h.serverError(w, r, "exchanging OAuth2 token", err)
		return
	}

	// Store refresh token in vault.
	if tokenResp.RefreshToken != "" {
		refreshKey := fmt.Sprintf("integrations/%s/refresh_token", integrationName)
		desc := fmt.Sprintf("%s OAuth2 refresh token", tmpl.DisplayName)
		if err := h.secrets.Set(r.Context(), agentID, refreshKey, tokenResp.RefreshToken, desc); err != nil {
			http.Error(w, "failed to store refresh token", http.StatusInternalServerError)
			return
		}
	}

	// Store access token JSON in vault.
	accessTokenJSON, err := json.Marshal(tokenResp)
	if err != nil {
		http.Error(w, "failed to marshal access token", http.StatusInternalServerError)
		return
	}
	accessKey := fmt.Sprintf("integrations/%s/access_token", integrationName)
	desc := fmt.Sprintf("%s OAuth2 access token", tmpl.DisplayName)
	if err := h.secrets.Set(r.Context(), agentID, accessKey, string(accessTokenJSON), desc); err != nil {
		http.Error(w, "failed to store access token", http.StatusInternalServerError)
		return
	}

	// Clear state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/integrations/"+integrationName+"?oauth=success", http.StatusSeeOther)
}

// signOAuthState produces a signed cookie value: state|integration|agent_id|expiry|hmac
func (h *Handlers) signOAuthState(state, integration, agentID string, expiry int64) string {
	payload := state + "|" + integration + "|" + agentID + "|" + strconv.FormatInt(expiry, 10)
	sig := h.sign(payload)
	return payload + "|" + sig
}

// validateOAuthState verifies the signed cookie and returns integration name + agent ID.
func (h *Handlers) validateOAuthState(cookieValue, expectedState string) (integration, agentID string, err error) {
	parts := strings.SplitN(cookieValue, "|", 5)
	if len(parts) != 5 {
		return "", "", fmt.Errorf("malformed state cookie")
	}

	state, integration, agentID, expiryStr, sig := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Verify state matches.
	if !hmac.Equal([]byte(state), []byte(expectedState)) {
		return "", "", fmt.Errorf("state mismatch")
	}

	// Verify HMAC.
	payload := state + "|" + integration + "|" + agentID + "|" + expiryStr
	expected := h.sign(payload)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", "", fmt.Errorf("invalid signature")
	}

	// Check expiry.
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", "", fmt.Errorf("invalid expiry")
	}
	if time.Now().Unix() > expiry {
		return "", "", fmt.Errorf("state expired")
	}

	return integration, agentID, nil
}

// oauthTokenResponse represents the token response from an OAuth2 provider.
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ObtainedAt   int64  `json:"obtained_at"`
}

// exchangeAuthCode performs the OAuth2 authorization_code grant.
func exchangeAuthCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI string) (*oauthTokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
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

	var result oauthTokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	result.ObtainedAt = time.Now().Unix()
	return &result, nil
}
