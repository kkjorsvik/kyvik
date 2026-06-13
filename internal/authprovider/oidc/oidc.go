// Package oidc implements the AuthProvider interface for self-hosted enterprise
// SSO via OpenID Connect. It handles PKCE-based authorization code flow,
// ID token verification, and automatic user provisioning on first login.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// oidcGroupName is the auto-provisioned group for OIDC users.
const oidcGroupName = "OIDC Users"

// stateTTL is how long a login state (nonce) remains valid.
const stateTTL = 10 * time.Minute

// Store is the subset of the data store required by the OIDC provider.
// It mirrors the delegated provider's Store interface with the addition
// of group CRUD needed for auto-provisioning.
type Store interface {
	CreateUser(ctx context.Context, user types.User) error
	GetUser(ctx context.Context, id string) (*types.User, error)
	UpdateUser(ctx context.Context, user types.User) error

	CreateSession(ctx context.Context, sess types.UserSession) error
	GetSession(ctx context.Context, id string) (*types.UserSession, error)
	DeleteSession(ctx context.Context, id string) error
	UpdateSessionLastSeen(ctx context.Context, id string, at time.Time) error

	ListUserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error)
	SetUserGroupRole(ctx context.Context, ugr types.UserGroupRole) error
	DeleteUserGroupRole(ctx context.Context, userID, groupID string) error
	ListGroupIDsByAgent(ctx context.Context, agentID string) ([]string, error)
	ListAgentIDsByGroup(ctx context.Context, groupID string) ([]string, error)

	// Group management for auto-provisioning.
	CreateAgentGroup(ctx context.Context, g types.AgentGroup) error
	ListAgentGroups(ctx context.Context) ([]types.AgentGroup, error)
}

// Config holds the configuration for the OIDC provider.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string        // e.g. "https://kyvik.example.com/auth/callback"
	DefaultRole  string        // default: "viewer"
	SessionTTL   time.Duration // default: 24h
}

// Provider implements authprovider.AuthProvider for OIDC-based authentication.
type Provider struct {
	store     Store
	cfg       Config
	oauth2Cfg oauth2.Config
	verifier  *gooidc.IDTokenVerifier
	states    *stateStore

	oidcGroupMu sync.Mutex
	oidcGroupID string // cached ID of the "OIDC Users" group
}

// stateStore holds in-memory OIDC authorization states with expiry.
type stateStore struct {
	mu     sync.Mutex
	states map[string]stateEntry
}

// stateEntry stores PKCE code verifier alongside its expiration time.
type stateEntry struct {
	codeVerifier string
	expiresAt    time.Time
}

// newStateStore creates an empty state store.
func newStateStore() *stateStore {
	return &stateStore{
		states: make(map[string]stateEntry),
	}
}

const maxPendingStates = 10000

// put stores a state with its associated PKCE code verifier.
// Returns an error if the store has too many pending states.
func (ss *stateStore) put(state, codeVerifier string) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.states) >= maxPendingStates {
		// Cleanup expired entries first.
		now := time.Now()
		for k, v := range ss.states {
			if now.After(v.expiresAt) {
				delete(ss.states, k)
			}
		}
		if len(ss.states) >= maxPendingStates {
			return fmt.Errorf("too many pending login attempts")
		}
	}
	ss.states[state] = stateEntry{
		codeVerifier: codeVerifier,
		expiresAt:    time.Now().Add(stateTTL),
	}
	return nil
}

// consume retrieves and removes a state entry, returning the code verifier.
// Returns empty string and false if the state is unknown or expired.
func (ss *stateStore) consume(state string) (codeVerifier string, ok bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	entry, exists := ss.states[state]
	if !exists {
		return "", false
	}
	delete(ss.states, state)
	if time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.codeVerifier, true
}

// cleanup removes all expired state entries.
func (ss *stateStore) cleanup() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := time.Now()
	for k, v := range ss.states {
		if now.After(v.expiresAt) {
			delete(ss.states, k)
		}
	}
}

// len returns the number of stored states (used in tests).
func (ss *stateStore) len() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.states)
}

// New creates an OIDC auth provider. It contacts the issuer to discover
// endpoints and download the JWKS for token verification.
func New(ctx context.Context, store Store, cfg Config) (*Provider, error) {
	if cfg.IssuerURL == "" {
		return nil, fmt.Errorf("oidc auth: issuer_url is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("oidc auth: client_id is required")
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("oidc auth: client_secret is required")
	}
	if cfg.RedirectURL == "" {
		return nil, fmt.Errorf("oidc auth: redirect_url is required")
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "viewer"
	}
	if !auth.IsDefaultRole(cfg.DefaultRole) {
		return nil, fmt.Errorf("oidc auth: invalid default_role %q", cfg.DefaultRole)
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}

	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{gooidc.ScopeOpenID, "profile", "email"},
	}

	verifier := provider.Verifier(&gooidc.Config{
		ClientID: cfg.ClientID,
	})

	return &Provider{
		store:     store,
		cfg:       cfg,
		oauth2Cfg: oauth2Cfg,
		verifier:  verifier,
		states:    newStateStore(),
	}, nil
}

// NewFromConfig creates an OIDC auth provider from the application config.
func NewFromConfig(ctx context.Context, store Store, ocfg config.OIDCAuthConfig, sessionTTL time.Duration) (*Provider, error) {
	return New(ctx, store, Config{
		IssuerURL:    ocfg.IssuerURL,
		ClientID:     ocfg.ClientID,
		ClientSecret: ocfg.ClientSecret,
		RedirectURL:  ocfg.RedirectURL,
		DefaultRole:  ocfg.DefaultRole,
		SessionTTL:   sessionTTL,
	})
}

// Login returns nil result and a redirect URL to the OIDC authorization
// endpoint. The URL includes PKCE (S256) and a random state parameter.
// Username and password are ignored for OIDC auth.
func (p *Provider) Login(_ context.Context, _, _, _, _ string) (*authprovider.LoginResult, string, error) {
	// Clean up expired states on each login attempt.
	p.states.cleanup()

	state, err := randomString(32)
	if err != nil {
		return nil, "", fmt.Errorf("generate state: %w", err)
	}
	codeVerifier, err := randomString(64)
	if err != nil {
		return nil, "", fmt.Errorf("generate code verifier: %w", err)
	}

	if err := p.states.put(state, codeVerifier); err != nil {
		return nil, "", fmt.Errorf("store state: %w", err)
	}

	// Build PKCE code challenge (S256).
	codeChallenge := s256Challenge(codeVerifier)

	authURL := p.oauth2Cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	return nil, authURL, nil
}

// HandleCallback exchanges the authorization code for tokens, verifies the
// ID token, upserts the user, and creates a local session.
func (p *Provider) HandleCallback(ctx context.Context, r *http.Request) (*authprovider.LoginResult, error) {
	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, fmt.Errorf("missing code parameter")
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		return nil, fmt.Errorf("missing state parameter")
	}

	// Verify state and recover code verifier.
	codeVerifier, ok := p.states.consume(state)
	if !ok {
		return nil, fmt.Errorf("invalid or expired state")
	}

	// Exchange authorization code for tokens with PKCE verifier.
	token, err := p.oauth2Cfg.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	// Extract and verify ID token.
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("no id_token in token response")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}

	// Extract claims.
	var claims struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	if claims.Sub == "" {
		return nil, fmt.Errorf("missing sub claim in id_token")
	}

	// Determine username: prefer email, fall back to preferred_username, then sub.
	username := claims.Email
	if username == "" {
		username = claims.PreferredUsername
	}
	if username == "" {
		username = claims.Sub
	}

	// Determine display name: prefer name, fall back to username.
	displayName := claims.Name
	if displayName == "" {
		displayName = username
	}

	// Derive a stable user ID from the issuer + sub.
	userID := deriveUserID(p.cfg.IssuerURL, claims.Sub)

	// Upsert user.
	user, err := p.upsertUser(ctx, userID, username, displayName)
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}

	// Create local session.
	now := time.Now().UTC()
	exp := now.Add(p.cfg.SessionTTL)
	sess := types.UserSession{
		ID:         uuid.NewString(),
		UserID:     user.ID,
		CreatedAt:  now,
		ExpiresAt:  exp,
		LastSeenAt: &now,
		IPAddress:  r.RemoteAddr,
		UserAgent:  r.UserAgent(),
	}
	if err := p.store.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &authprovider.LoginResult{
		SessionID: sess.ID,
		UserID:    user.ID,
		Username:  user.Username,
		ExpiresAt: exp,
	}, nil
}

// ValidateSession verifies session validity and returns the owning user.
func (p *Provider) ValidateSession(ctx context.Context, sessionID string) (*types.User, error) {
	if sessionID == "" {
		return nil, types.ErrSessionNotFound
	}

	sess, err := p.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(now) {
		_ = p.store.DeleteSession(ctx, sessionID)
		return nil, types.ErrSessionNotFound
	}
	if sess.LastSeenAt == nil || now.Sub(*sess.LastSeenAt) >= time.Minute {
		_ = p.store.UpdateSessionLastSeen(ctx, sessionID, now)
	}

	user, err := p.store.GetUser(ctx, sess.UserID)
	if err != nil {
		_ = p.store.DeleteSession(ctx, sessionID)
		return nil, err
	}
	if !user.IsActive {
		_ = p.store.DeleteSession(ctx, sessionID)
		return nil, types.ErrPermissionDenied
	}
	return user, nil
}

// Logout invalidates a session in the local store.
func (p *Provider) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := p.store.DeleteSession(ctx, sessionID); err != nil && !errors.Is(err, types.ErrSessionNotFound) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// SessionTTL returns the configured session lifetime.
func (p *Provider) SessionTTL() time.Duration {
	return p.cfg.SessionTTL
}

// ResolveGlobalRole returns the highest default role granted to the user
// across all of their user_group_roles records.
func (p *Provider) ResolveGlobalRole(ctx context.Context, userID string) (string, bool, error) {
	roles, err := p.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return "", false, fmt.Errorf("list user group roles: %w", err)
	}
	top := highestDefaultRole(roles)
	if top == "" {
		return "", false, nil
	}
	return top, true, nil
}

// ResolveAgentRole returns the highest role for a user on a specific agent,
// based on overlapping user group memberships and agent group memberships.
func (p *Provider) ResolveAgentRole(ctx context.Context, userID, agentID string) (string, bool, error) {
	userRoles, err := p.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return "", false, fmt.Errorf("list user group roles: %w", err)
	}
	agentGroupIDs, err := p.store.ListGroupIDsByAgent(ctx, agentID)
	if err != nil {
		return "", false, fmt.Errorf("list agent groups: %w", err)
	}
	if len(agentGroupIDs) == 0 || len(userRoles) == 0 {
		return "", false, nil
	}

	groupSet := make(map[string]struct{}, len(agentGroupIDs))
	for _, gid := range agentGroupIDs {
		groupSet[gid] = struct{}{}
	}
	var matching []types.UserGroupRole
	for _, ugr := range userRoles {
		if _, ok := groupSet[ugr.GroupID]; ok {
			matching = append(matching, ugr)
		}
	}
	top := highestDefaultRole(matching)
	if top == "" {
		return "", false, nil
	}
	return top, true, nil
}

// ListVisibleAgentIDs returns all agent IDs visible to the user via group membership.
func (p *Provider) ListVisibleAgentIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	userRoles, err := p.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user group roles: %w", err)
	}
	agentIDs := make(map[string]struct{})
	for _, ugr := range userRoles {
		ids, listErr := p.store.ListAgentIDsByGroup(ctx, ugr.GroupID)
		if listErr != nil {
			return nil, fmt.Errorf("list agents by group %s: %w", ugr.GroupID, listErr)
		}
		for _, id := range ids {
			agentIDs[id] = struct{}{}
		}
	}
	return agentIDs, nil
}

// Capabilities reports that this provider delegates user management to the OIDC IdP.
func (p *Provider) Capabilities() authprovider.ProviderCapabilities {
	return authprovider.ProviderCapabilities{
		CanManageUsers:     false,
		CanManagePasswords: false,
		CanChangePassword:  false,
		LoginMode:          "redirect",
	}
}

// upsertUser creates or updates a user from OIDC claims.
// On first login, the user is also assigned to the "OIDC Users" group.
func (p *Provider) upsertUser(ctx context.Context, userID, username, displayName string) (*types.User, error) {
	now := time.Now().UTC()

	existing, err := p.store.GetUser(ctx, userID)
	if err != nil && !errors.Is(err, types.ErrUserNotFound) {
		return nil, fmt.Errorf("get user: %w", err)
	}

	if errors.Is(err, types.ErrUserNotFound) {
		// Create new user.
		user := types.User{
			ID:          userID,
			Username:    username,
			DisplayName: displayName,
			IsAdmin:     false,
			IsActive:    true,
			CreatedAt:   now,
			LastLoginAt: &now,
		}
		if err := p.store.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}

		// Auto-provision: create or find "OIDC Users" group and assign default role.
		groupID, err := p.ensureOIDCGroup(ctx)
		if err != nil {
			return nil, fmt.Errorf("ensure oidc group: %w", err)
		}
		if err := p.store.SetUserGroupRole(ctx, types.UserGroupRole{
			UserID:  userID,
			GroupID: groupID,
			Role:    p.cfg.DefaultRole,
		}); err != nil {
			return nil, fmt.Errorf("set default role: %w", err)
		}

		return &user, nil
	}

	// Update existing user's username and display name if changed.
	if username != "" && username != existing.Username {
		existing.Username = username
	}
	if displayName != "" && displayName != existing.DisplayName {
		existing.DisplayName = displayName
	}
	existing.LastLoginAt = &now
	if err := p.store.UpdateUser(ctx, *existing); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	return existing, nil
}

// ensureOIDCGroup returns the ID of the "OIDC Users" group, creating it
// if it does not already exist. The group ID is cached after first lookup.
func (p *Provider) ensureOIDCGroup(ctx context.Context) (string, error) {
	p.oidcGroupMu.Lock()
	defer p.oidcGroupMu.Unlock()
	if p.oidcGroupID != "" {
		return p.oidcGroupID, nil
	}
	groups, err := p.store.ListAgentGroups(ctx)
	if err != nil {
		return "", fmt.Errorf("list groups: %w", err)
	}
	for _, g := range groups {
		if g.Name == oidcGroupName {
			p.oidcGroupID = g.ID
			return g.ID, nil
		}
	}

	// Create the group.
	g := types.AgentGroup{
		ID:          uuid.NewString(),
		Name:        oidcGroupName,
		Description: "Auto-provisioned group for OIDC-authenticated users",
		CreatedAt:   time.Now().UTC(),
	}
	if err := p.store.CreateAgentGroup(ctx, g); err != nil {
		return "", fmt.Errorf("create group: %w", err)
	}
	p.oidcGroupID = g.ID
	return g.ID, nil
}

// deriveUserID creates a deterministic UUID-like ID from the OIDC issuer and sub claim.
func deriveUserID(issuer, sub string) string {
	h := sha256.Sum256([]byte(issuer + "|" + sub))
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// s256Challenge computes the S256 PKCE code challenge from a code verifier.
func s256Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// randomString generates a cryptographically random URL-safe string of n bytes.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// highestDefaultRole returns the most permissive role from a slice of UserGroupRoles.
func highestDefaultRole(roles []types.UserGroupRole) string {
	top := ""
	for _, ugr := range roles {
		if !auth.IsDefaultRole(ugr.Role) {
			continue
		}
		if top == "" {
			top = ugr.Role
			continue
		}
		top = auth.MorePermissiveRole(top, ugr.Role)
	}
	return top
}
