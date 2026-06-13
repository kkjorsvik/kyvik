package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ authprovider.AuthProvider = (*Provider)(nil)

// --- Mock OIDC server infrastructure ---

// mockOIDCServer provides a complete mock OIDC identity provider for testing.
type mockOIDCServer struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	issuerURL  string
	// mu protects currentIDToken, allowing tests to swap the token response.
	mu             sync.Mutex
	currentIDToken string
}

// idTokenClaims defines the claims embedded in test ID tokens.
type idTokenClaims struct {
	Issuer            string `json:"iss"`
	Subject           string `json:"sub"`
	Audience          string `json:"aud"`
	Email             string `json:"email,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Name              string `json:"name,omitempty"`
	IssuedAt          int64  `json:"iat"`
	Expiry            int64  `json:"exp"`
	Nonce             string `json:"nonce,omitempty"`
}

func newMockOIDCServer(t *testing.T) *mockOIDCServer {
	t.Helper()

	// Generate RSA key pair for signing tokens.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	m := &mockOIDCServer{
		privateKey: key,
	}

	mux := http.NewServeMux()
	m.server = httptest.NewServer(mux)
	m.issuerURL = m.server.URL
	t.Cleanup(m.server.Close)

	// Discovery endpoint.
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		disc := map[string]interface{}{
			"issuer":                 m.issuerURL,
			"authorization_endpoint": m.issuerURL + "/authorize",
			"token_endpoint":         m.issuerURL + "/token",
			"jwks_uri":               m.issuerURL + "/jwks",
			"userinfo_endpoint":      m.issuerURL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(disc)
	})

	// JWKS endpoint.
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwk := jose.JSONWebKey{
			Key:       &key.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{jwk},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	})

	// Token endpoint: returns the current ID token (mutable via installTokenHandler).
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", 400)
			return
		}
		m.mu.Lock()
		idToken := m.currentIDToken
		m.mu.Unlock()
		resp := map[string]interface{}{
			"access_token":  "mock-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      idToken,
			"refresh_token": "mock-refresh-token",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	return m
}

// signIDToken creates a signed JWT ID token with the given claims.
func (m *mockOIDCServer) signIDToken(t *testing.T, claims idTokenClaims) string {
	t.Helper()

	signerKey := jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       m.privateKey,
	}
	signer, err := jose.NewSigner(signerKey, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"))
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

// installTokenHandler sets the ID token that the /token endpoint returns.
// It can be called multiple times to change the token response for different
// test scenarios (e.g., testing user updates on re-login).
func (m *mockOIDCServer) installTokenHandler(t *testing.T, claims idTokenClaims, clientID string) {
	t.Helper()
	idToken := m.signIDToken(t, claims)
	m.mu.Lock()
	m.currentIDToken = idToken
	m.mu.Unlock()
}

// --- Test helpers ---

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
)

// newTestProvider creates an OIDC provider backed by a mock OIDC server and
// a PostgreSQL store. It returns the provider, store, and mock server.
func newTestProvider(t *testing.T, claims idTokenClaims) (*Provider, *postgres.PostgresStore, *mockOIDCServer) {
	t.Helper()

	mock := newMockOIDCServer(t)

	// Fill in issuer-dependent claim defaults.
	if claims.Issuer == "" {
		claims.Issuer = mock.issuerURL
	}
	if claims.Audience == "" {
		claims.Audience = testClientID
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.Expiry == 0 {
		claims.Expiry = time.Now().Add(10 * time.Minute).Unix()
	}

	mock.installTokenHandler(t, claims, testClientID)

	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	redirectURL := mock.issuerURL + "/callback"

	ctx := context.Background()
	// Override the OIDC HTTP client to use the test server's client (trusts its TLS).
	ctx = gooidc.ClientContext(ctx, mock.server.Client())

	p, err := New(ctx, s, Config{
		IssuerURL:    mock.issuerURL,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		RedirectURL:  redirectURL,
		DefaultRole:  "viewer",
		SessionTTL:   24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	return p, s, mock
}

// defaultClaims returns basic OIDC ID token claims for testing.
// IssuerURL and Audience are filled in by newTestProvider.
func defaultClaims() idTokenClaims {
	return idTokenClaims{
		Subject:           "oidc-user-sub-123",
		Email:             "alice@example.com",
		PreferredUsername: "alice",
		Name:              "Alice Smith",
	}
}

// simulateCallback creates a callback request with code and state, inserting
// the state into the provider's state store with a matching code verifier.
func simulateCallback(t *testing.T, p *Provider, code, state, codeVerifier string) *http.Request {
	t.Helper()
	p.states.put(state, codeVerifier)
	u := fmt.Sprintf("https://kyvik.example.com/auth/callback?code=%s&state=%s",
		url.QueryEscape(code), url.QueryEscape(state))
	return httptest.NewRequest(http.MethodGet, u, nil)
}

// --- Constructor validation tests ---

func TestNew_RejectsEmptyIssuerURL(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := New(context.Background(), tdb.Store, Config{
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		RedirectURL:  "https://example.com/callback",
	})
	if err == nil {
		t.Fatal("expected error for empty IssuerURL")
	}
	if !strings.Contains(err.Error(), "issuer_url") {
		t.Errorf("error = %q, want mention of 'issuer_url'", err)
	}
}

func TestNew_RejectsEmptyClientID(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := New(context.Background(), tdb.Store, Config{
		IssuerURL:    "https://idp.example.com",
		ClientSecret: testClientSecret,
		RedirectURL:  "https://example.com/callback",
	})
	if err == nil {
		t.Fatal("expected error for empty ClientID")
	}
	if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error = %q, want mention of 'client_id'", err)
	}
}

func TestNew_RejectsEmptyClientSecret(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := New(context.Background(), tdb.Store, Config{
		IssuerURL:   "https://idp.example.com",
		ClientID:    testClientID,
		RedirectURL: "https://example.com/callback",
	})
	if err == nil {
		t.Fatal("expected error for empty ClientSecret")
	}
	if !strings.Contains(err.Error(), "client_secret") {
		t.Errorf("error = %q, want mention of 'client_secret'", err)
	}
}

func TestNew_RejectsEmptyRedirectURL(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := New(context.Background(), tdb.Store, Config{
		IssuerURL:    "https://idp.example.com",
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
	})
	if err == nil {
		t.Fatal("expected error for empty RedirectURL")
	}
	if !strings.Contains(err.Error(), "redirect_url") {
		t.Errorf("error = %q, want mention of 'redirect_url'", err)
	}
}

func TestNew_RejectsInvalidDefaultRole(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := New(context.Background(), tdb.Store, Config{
		IssuerURL:    "https://idp.example.com",
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		RedirectURL:  "https://example.com/callback",
		DefaultRole:  "superuser",
	})
	if err == nil {
		t.Fatal("expected error for invalid DefaultRole")
	}
	if !strings.Contains(err.Error(), "default_role") {
		t.Errorf("error = %q, want mention of 'default_role'", err)
	}
}

// --- Login tests ---

func TestLogin_ReturnsOIDCAuthURL(t *testing.T) {
	p, _, mock := newTestProvider(t, defaultClaims())

	result, redirectURL, err := p.Login(context.Background(), "", "", "", "")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if result != nil {
		t.Fatalf("Login() should return nil result for redirect, got %+v", result)
	}

	// Parse the redirect URL.
	u, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}

	// Should point to the mock server's authorize endpoint.
	expectedBase := mock.issuerURL + "/authorize"
	if !strings.HasPrefix(redirectURL, expectedBase) {
		t.Errorf("redirect URL should start with %s, got %s", expectedBase, redirectURL)
	}

	// Check required parameters.
	q := u.Query()
	if q.Get("client_id") != testClientID {
		t.Errorf("client_id = %q, want %q", q.Get("client_id"), testClientID)
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want %q", q.Get("response_type"), "code")
	}
	if q.Get("state") == "" {
		t.Error("state parameter is missing")
	}
	if q.Get("code_challenge") == "" {
		t.Error("code_challenge parameter is missing (PKCE)")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want %q", q.Get("code_challenge_method"), "S256")
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope = %q, should contain 'openid'", q.Get("scope"))
	}
}

func TestLogin_StoresState(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())

	// State store should be empty initially.
	if got := p.states.len(); got != 0 {
		t.Fatalf("initial state count = %d, want 0", got)
	}

	_, _, err := p.Login(context.Background(), "", "", "", "")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	if got := p.states.len(); got != 1 {
		t.Errorf("state count after Login = %d, want 1", got)
	}
}

func TestLogin_CleansUpExpiredStates(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())

	// Manually insert an expired state.
	p.states.mu.Lock()
	p.states.states["expired-state"] = stateEntry{
		codeVerifier: "verifier",
		expiresAt:    time.Now().Add(-time.Minute),
	}
	p.states.mu.Unlock()

	if got := p.states.len(); got != 1 {
		t.Fatalf("pre-login state count = %d, want 1", got)
	}

	_, _, err := p.Login(context.Background(), "", "", "", "")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	// Should have the new state but not the expired one.
	if got := p.states.len(); got != 1 {
		t.Errorf("post-login state count = %d, want 1 (expired should be cleaned)", got)
	}

	// The expired state should be gone.
	p.states.mu.Lock()
	_, exists := p.states.states["expired-state"]
	p.states.mu.Unlock()
	if exists {
		t.Error("expired state should have been cleaned up")
	}
}

// --- HandleCallback tests ---

func TestHandleCallback_ValidCode(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := context.Background()

	// Override the HTTP client for OIDC token exchange.
	ctx = gooidc.ClientContext(ctx, mock.server.Client())

	r := simulateCallback(t, p, "valid-auth-code", "test-state", "test-verifier")

	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Verify result fields.
	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	if result.UserID != expectedUserID {
		t.Errorf("UserID = %q, want %q", result.UserID, expectedUserID)
	}
	if result.Username != claims.Email {
		t.Errorf("Username = %q, want %q", result.Username, claims.Email)
	}
	if result.SessionID == "" {
		t.Error("SessionID is empty")
	}
	if result.ForcePasswordChange {
		t.Error("ForcePasswordChange should be false for OIDC auth")
	}
	if result.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}

	// Verify user was created in the store.
	user, err := s.GetUser(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("GetUser() error: %v", err)
	}
	if user.Username != claims.Email {
		t.Errorf("stored Username = %q, want %q", user.Username, claims.Email)
	}
	if user.DisplayName != claims.Name {
		t.Errorf("stored DisplayName = %q, want %q", user.DisplayName, claims.Name)
	}
	if user.IsAdmin {
		t.Error("stored user should not be admin")
	}
	if !user.IsActive {
		t.Error("stored user should be active")
	}
	if user.PasswordHash != "" {
		t.Error("OIDC user should have empty PasswordHash")
	}

	// Verify session was created.
	sess, err := s.GetSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if sess.UserID != expectedUserID {
		t.Errorf("session UserID = %q, want %q", sess.UserID, expectedUserID)
	}
}

func TestHandleCallback_UserProvisioning(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "valid-auth-code", "test-state", "test-verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Verify "OIDC Users" group was created.
	groups, err := s.ListAgentGroups(ctx)
	if err != nil {
		t.Fatalf("ListAgentGroups() error: %v", err)
	}
	var oidcGroup *types.AgentGroup
	for i := range groups {
		if groups[i].Name == oidcGroupName {
			oidcGroup = &groups[i]
			break
		}
	}
	if oidcGroup == nil {
		t.Fatal("OIDC Users group was not created")
	}

	// Verify user has viewer role in the OIDC group.
	roles, err := s.ListUserGroupRoles(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("ListUserGroupRoles() error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}
	if roles[0].GroupID != oidcGroup.ID {
		t.Errorf("role group = %q, want %q", roles[0].GroupID, oidcGroup.ID)
	}
	if roles[0].Role != "viewer" {
		t.Errorf("role = %q, want %q", roles[0].Role, "viewer")
	}
}

func TestHandleCallback_ExistingUserUpdated(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	// First login creates the user.
	r := simulateCallback(t, p, "code-1", "state-1", "verifier-1")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("first HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Verify initial display name.
	user, err := s.GetUser(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("GetUser() error: %v", err)
	}
	if user.DisplayName != claims.Name {
		t.Errorf("initial DisplayName = %q, want %q", user.DisplayName, claims.Name)
	}
	originalCreatedAt := user.CreatedAt

	// Update the mock to return tokens with updated claims for the same sub.
	updatedClaims := claims
	updatedClaims.Issuer = mock.issuerURL
	updatedClaims.Audience = testClientID
	updatedClaims.IssuedAt = time.Now().Unix()
	updatedClaims.Expiry = time.Now().Add(10 * time.Minute).Unix()
	updatedClaims.Name = "Alice Updated"
	mock.installTokenHandler(t, updatedClaims, testClientID)

	// Second login should update.
	r = simulateCallback(t, p, "code-2", "state-2", "verifier-2")
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("second HandleCallback() error: %v", err)
	}
	if result.UserID != expectedUserID {
		t.Errorf("UserID = %q, want %q", result.UserID, expectedUserID)
	}

	// Verify display name was updated.
	user, err = s.GetUser(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("GetUser() after update error: %v", err)
	}
	if user.DisplayName != "Alice Updated" {
		t.Errorf("updated DisplayName = %q, want %q", user.DisplayName, "Alice Updated")
	}
	// CreatedAt should be preserved.
	if !user.CreatedAt.Equal(originalCreatedAt) {
		t.Error("CreatedAt should be preserved from original user")
	}
}

func TestHandleCallback_SecondLoginDoesNotDuplicateGroup(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	// First login.
	r := simulateCallback(t, p, "code-1", "state-1", "verifier-1")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("first HandleCallback() error: %v", err)
	}

	// Count groups.
	groups1, _ := s.ListAgentGroups(ctx)
	count1 := 0
	for _, g := range groups1 {
		if g.Name == oidcGroupName {
			count1++
		}
	}

	// Second login with a different user (same provider).
	differentClaims := defaultClaims()
	differentClaims.Issuer = mock.issuerURL
	differentClaims.Audience = testClientID
	differentClaims.IssuedAt = time.Now().Unix()
	differentClaims.Expiry = time.Now().Add(10 * time.Minute).Unix()
	differentClaims.Subject = "different-sub"
	differentClaims.Email = "bob@example.com"
	differentClaims.Name = "Bob"
	mock.installTokenHandler(t, differentClaims, testClientID)

	r = simulateCallback(t, p, "code-2", "state-2", "verifier-2")
	_, err = p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("second HandleCallback() error: %v", err)
	}

	// Count groups again.
	groups2, _ := s.ListAgentGroups(ctx)
	count2 := 0
	for _, g := range groups2 {
		if g.Name == oidcGroupName {
			count2++
		}
	}
	if count2 != count1 {
		t.Errorf("OIDC Users group count: first login = %d, second login = %d (should be same)", count1, count2)
	}
}

func TestHandleCallback_MissingCode(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	r := httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback?state=foo", nil)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing code")
	}
	if !strings.Contains(err.Error(), "code") {
		t.Errorf("error = %q, want mention of 'code'", err)
	}
}

func TestHandleCallback_MissingState(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	r := httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback?code=foo", nil)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing state")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("error = %q, want mention of 'state'", err)
	}
}

func TestHandleCallback_InvalidState(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	r := httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback?code=foo&state=unknown-state", nil)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
	if !strings.Contains(err.Error(), "invalid or expired state") {
		t.Errorf("error = %q, want mention of 'invalid or expired state'", err)
	}
}

func TestHandleCallback_ExpiredState(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())

	// Insert an expired state manually.
	p.states.mu.Lock()
	p.states.states["expired-state"] = stateEntry{
		codeVerifier: "verifier",
		expiresAt:    time.Now().Add(-time.Minute),
	}
	p.states.mu.Unlock()

	r := httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback?code=foo&state=expired-state", nil)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for expired state")
	}
	if !strings.Contains(err.Error(), "invalid or expired state") {
		t.Errorf("error = %q, want mention of 'invalid or expired state'", err)
	}
}

func TestHandleCallback_StateConsumedOnce(t *testing.T) {
	p, _, mock := newTestProvider(t, defaultClaims())
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	// First callback succeeds.
	r := simulateCallback(t, p, "code-1", "one-time-state", "verifier-1")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("first HandleCallback() error: %v", err)
	}

	// Same state should fail (it was consumed).
	r = httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback?code=code-2&state=one-time-state", nil)
	_, err = p.HandleCallback(ctx, r)
	if err == nil {
		t.Fatal("expected error for reused state")
	}
}

func TestHandleCallback_ClaimsWithNoEmail(t *testing.T) {
	claims := defaultClaims()
	claims.Email = "" // No email, should fall back to preferred_username.
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	user, _ := s.GetUser(ctx, expectedUserID)
	if user.Username != claims.PreferredUsername {
		t.Errorf("Username = %q, want %q (preferred_username fallback)", user.Username, claims.PreferredUsername)
	}
	if result.Username != claims.PreferredUsername {
		t.Errorf("result.Username = %q, want %q", result.Username, claims.PreferredUsername)
	}
}

func TestHandleCallback_ClaimsWithNoEmailOrPreferredUsername(t *testing.T) {
	claims := defaultClaims()
	claims.Email = ""
	claims.PreferredUsername = ""
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	user, _ := s.GetUser(ctx, expectedUserID)
	// Should fall back to the sub claim.
	if user.Username != claims.Subject {
		t.Errorf("Username = %q, want %q (sub fallback)", user.Username, claims.Subject)
	}
}

func TestHandleCallback_ClaimsWithNoName(t *testing.T) {
	claims := defaultClaims()
	claims.Name = "" // No name, should use username as display name.
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	user, _ := s.GetUser(ctx, expectedUserID)
	if user.DisplayName != claims.Email {
		t.Errorf("DisplayName = %q, want %q (username fallback)", user.DisplayName, claims.Email)
	}
}

// --- ValidateSession tests ---

func TestValidateSession_Valid(t *testing.T) {
	claims := defaultClaims()
	p, _, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	user, err := p.ValidateSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ValidateSession() error: %v", err)
	}
	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	if user.ID != expectedUserID {
		t.Errorf("user.ID = %q, want %q", user.ID, expectedUserID)
	}
}

func TestValidateSession_EmptyID(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	_, err := p.ValidateSession(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty session ID")
	}
}

func TestValidateSession_NonexistentSession(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	_, err := p.ValidateSession(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestValidateSession_InactiveUser(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Deactivate the user.
	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)
	user, _ := s.GetUser(ctx, expectedUserID)
	user.IsActive = false
	if err := s.UpdateUser(ctx, *user); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}

	_, err = p.ValidateSession(ctx, result.SessionID)
	if err == nil {
		t.Fatal("expected error for inactive user")
	}
}

// --- Logout tests ---

func TestLogout(t *testing.T) {
	claims := defaultClaims()
	p, _, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	if err := p.Logout(ctx, result.SessionID); err != nil {
		t.Fatalf("Logout() error: %v", err)
	}

	// Session should be invalid now.
	_, err = p.ValidateSession(ctx, result.SessionID)
	if err == nil {
		t.Fatal("expected error after logout")
	}
}

func TestLogout_EmptySessionID(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	if err := p.Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout('') error: %v", err)
	}
}

// --- SessionTTL tests ---

func TestSessionTTL(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	if got := p.SessionTTL(); got != 24*time.Hour {
		t.Errorf("SessionTTL() = %v, want %v", got, 24*time.Hour)
	}
}

// --- Capabilities tests ---

func TestCapabilities(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	caps := p.Capabilities()

	if caps.CanManageUsers {
		t.Error("CanManageUsers should be false")
	}
	if caps.CanManagePasswords {
		t.Error("CanManagePasswords should be false")
	}
	if caps.CanChangePassword {
		t.Error("CanChangePassword should be false")
	}
	if caps.LoginMode != "redirect" {
		t.Errorf("LoginMode = %q, want %q", caps.LoginMode, "redirect")
	}
}

// --- Role resolution tests ---

func TestResolveGlobalRole(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	// Create user.
	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Add a second role in a different group.
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "extra-group", Name: "Extra"}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{
		UserID: expectedUserID, GroupID: "extra-group", Role: "admin",
	}); err != nil {
		t.Fatalf("set role: %v", err)
	}

	role, found, err := p.ResolveGlobalRole(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("ResolveGlobalRole() error: %v", err)
	}
	if !found {
		t.Fatal("expected role to be found")
	}
	// admin > viewer, so admin should win.
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
}

func TestResolveGlobalRole_NoRoles(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	_, found, err := p.ResolveGlobalRole(context.Background(), "nonexistent-user")
	if err != nil {
		t.Fatalf("ResolveGlobalRole() error: %v", err)
	}
	if found {
		t.Error("expected no role found for nonexistent user")
	}
}

func TestResolveAgentRole(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Find the OIDC Users group.
	groups, _ := s.ListAgentGroups(ctx)
	var oidcGroupID string
	for _, g := range groups {
		if g.Name == oidcGroupName {
			oidcGroupID = g.ID
			break
		}
	}

	// Create agent and place it in the OIDC group.
	if err := s.CreateAgent(ctx, types.AgentConfig{ID: "agent-A", Name: "agent-A"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-A", oidcGroupID); err != nil {
		t.Fatalf("set agent group: %v", err)
	}

	role, found, err := p.ResolveAgentRole(ctx, expectedUserID, "agent-A")
	if err != nil {
		t.Fatalf("ResolveAgentRole() error: %v", err)
	}
	if !found {
		t.Fatal("expected role to be found")
	}
	if role != "viewer" {
		t.Errorf("role = %q, want %q", role, "viewer")
	}
}

func TestResolveAgentRole_NoOverlap(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Create agent in a different group.
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "other-group", Name: "Other"}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := s.CreateAgent(ctx, types.AgentConfig{ID: "agent-B", Name: "agent-B"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-B", "other-group"); err != nil {
		t.Fatalf("set agent group: %v", err)
	}

	_, found, err := p.ResolveAgentRole(ctx, expectedUserID, "agent-B")
	if err != nil {
		t.Fatalf("ResolveAgentRole() error: %v", err)
	}
	if found {
		t.Error("expected no role found when groups don't overlap")
	}
}

func TestListVisibleAgentIDs(t *testing.T) {
	claims := defaultClaims()
	p, s, mock := newTestProvider(t, claims)
	ctx := gooidc.ClientContext(context.Background(), mock.server.Client())

	r := simulateCallback(t, p, "code", "state", "verifier")
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	expectedUserID := deriveUserID(mock.issuerURL, claims.Subject)

	// Find OIDC group.
	groups, _ := s.ListAgentGroups(ctx)
	var oidcGroupID string
	for _, g := range groups {
		if g.Name == oidcGroupName {
			oidcGroupID = g.ID
			break
		}
	}

	// Create agents.
	if err := s.CreateAgent(ctx, types.AgentConfig{ID: "agent-X", Name: "X"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateAgent(ctx, types.AgentConfig{ID: "agent-Y", Name: "Y"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// agent-X in OIDC group, agent-Y in a different group.
	if err := s.SetAgentGroupMember(ctx, "agent-X", oidcGroupID); err != nil {
		t.Fatalf("set agent X group: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "other-group", Name: "Other"}); err != nil {
		t.Fatalf("create other group: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-Y", "other-group"); err != nil {
		t.Fatalf("set agent Y group: %v", err)
	}

	visible, err := p.ListVisibleAgentIDs(ctx, expectedUserID)
	if err != nil {
		t.Fatalf("ListVisibleAgentIDs() error: %v", err)
	}

	if _, ok := visible["agent-X"]; !ok {
		t.Error("agent-X should be visible (in OIDC group)")
	}
	if _, ok := visible["agent-Y"]; ok {
		t.Error("agent-Y should NOT be visible (in different group)")
	}
}

func TestListVisibleAgentIDs_NoGroups(t *testing.T) {
	p, _, _ := newTestProvider(t, defaultClaims())
	visible, err := p.ListVisibleAgentIDs(context.Background(), "nonexistent-user")
	if err != nil {
		t.Fatalf("ListVisibleAgentIDs() error: %v", err)
	}
	if len(visible) != 0 {
		t.Errorf("expected 0 visible agents, got %d", len(visible))
	}
}

// --- ID derivation tests ---

func TestDeriveUserID_Deterministic(t *testing.T) {
	id1 := deriveUserID("https://idp.example.com", "sub-123")
	id2 := deriveUserID("https://idp.example.com", "sub-123")
	if id1 != id2 {
		t.Errorf("deriveUserID not deterministic: %q != %q", id1, id2)
	}
}

func TestDeriveUserID_DifferentIssuers(t *testing.T) {
	id1 := deriveUserID("https://idp1.example.com", "sub-123")
	id2 := deriveUserID("https://idp2.example.com", "sub-123")
	if id1 == id2 {
		t.Error("same sub from different issuers should produce different IDs")
	}
}

func TestDeriveUserID_DifferentSubs(t *testing.T) {
	id1 := deriveUserID("https://idp.example.com", "sub-123")
	id2 := deriveUserID("https://idp.example.com", "sub-456")
	if id1 == id2 {
		t.Error("different subs from same issuer should produce different IDs")
	}
}

func TestDeriveUserID_Format(t *testing.T) {
	id := deriveUserID("https://idp.example.com", "test-sub")
	// Should be hex format: 8-4-4-4-12 characters.
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("deriveUserID format: expected 5 dash-separated parts, got %d: %q", len(parts), id)
	}
	expectedLens := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != expectedLens[i] {
			t.Errorf("part %d length = %d, want %d (value: %q)", i, len(part), expectedLens[i], part)
		}
	}
}

// --- S256 challenge test ---

func TestS256Challenge(t *testing.T) {
	// Verify that s256Challenge produces the correct SHA-256 hash.
	verifier := "test-code-verifier"
	challenge := s256Challenge(verifier)

	// Compute expected value manually.
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])

	if challenge != expected {
		t.Errorf("s256Challenge(%q) = %q, want %q", verifier, challenge, expected)
	}
}

// --- State store tests ---

func TestStateStore_PutAndConsume(t *testing.T) {
	ss := newStateStore()
	ss.put("state-1", "verifier-1")

	verifier, ok := ss.consume("state-1")
	if !ok {
		t.Fatal("expected state to be found")
	}
	if verifier != "verifier-1" {
		t.Errorf("verifier = %q, want %q", verifier, "verifier-1")
	}

	// Second consume should fail.
	_, ok = ss.consume("state-1")
	if ok {
		t.Error("state should be consumed (single use)")
	}
}

func TestStateStore_UnknownState(t *testing.T) {
	ss := newStateStore()
	_, ok := ss.consume("unknown")
	if ok {
		t.Error("expected unknown state to not be found")
	}
}

func TestStateStore_ExpiredState(t *testing.T) {
	ss := newStateStore()
	ss.mu.Lock()
	ss.states["expired"] = stateEntry{
		codeVerifier: "verifier",
		expiresAt:    time.Now().Add(-time.Minute),
	}
	ss.mu.Unlock()

	_, ok := ss.consume("expired")
	if ok {
		t.Error("expected expired state to not be found")
	}
}

func TestStateStore_Cleanup(t *testing.T) {
	ss := newStateStore()

	// Add a valid state and an expired state.
	ss.put("valid", "verifier-valid")
	ss.mu.Lock()
	ss.states["expired-1"] = stateEntry{
		codeVerifier: "v1",
		expiresAt:    time.Now().Add(-time.Minute),
	}
	ss.states["expired-2"] = stateEntry{
		codeVerifier: "v2",
		expiresAt:    time.Now().Add(-time.Hour),
	}
	ss.mu.Unlock()

	if ss.len() != 3 {
		t.Fatalf("pre-cleanup len = %d, want 3", ss.len())
	}

	ss.cleanup()

	if ss.len() != 1 {
		t.Errorf("post-cleanup len = %d, want 1", ss.len())
	}

	// Valid state should still be consumable.
	verifier, ok := ss.consume("valid")
	if !ok {
		t.Fatal("valid state should survive cleanup")
	}
	if verifier != "verifier-valid" {
		t.Errorf("verifier = %q, want %q", verifier, "verifier-valid")
	}
}
