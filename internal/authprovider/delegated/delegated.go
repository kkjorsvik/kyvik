// Package delegated implements the AuthProvider interface for Sett-managed
// Kyvik instances. Login redirects to Sett, which authenticates the user
// (via WorkOS/SSO) and redirects back with a signed JWT assertion.
package delegated

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Store is the subset of users.Store required by the delegated provider.
type Store interface {
	CreateUser(ctx context.Context, user types.User) error
	GetUser(ctx context.Context, id string) (*types.User, error)
	UpdateUser(ctx context.Context, user types.User) error

	CreateSession(ctx context.Context, sess types.UserSession) error
	GetSession(ctx context.Context, id string) (*types.UserSession, error)
	DeleteSession(ctx context.Context, id string) error
	UpdateSessionLastSeen(ctx context.Context, id string, at time.Time) error
	DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error)

	ListUserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error)
	SetUserGroupRole(ctx context.Context, ugr types.UserGroupRole) error
	DeleteUserGroupRole(ctx context.Context, userID, groupID string) error
	ListGroupIDsByAgent(ctx context.Context, agentID string) ([]string, error)
	ListAgentIDsByGroup(ctx context.Context, groupID string) ([]string, error)
}

// Config holds the configuration for the delegated provider.
type Config struct {
	SettURL      string
	InstanceID   string
	SharedSecret string
	CallbackURL  string
	SessionTTL   time.Duration
}

// Provider implements authprovider.AuthProvider for Sett-delegated auth.
type Provider struct {
	store Store
	cfg   Config
}

// New creates a delegated auth provider. It returns an error if any required
// configuration field is empty.
func New(store Store, cfg Config) (*Provider, error) {
	if cfg.SettURL == "" {
		return nil, fmt.Errorf("delegated auth: sett_url is required")
	}
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("delegated auth: instance_id is required")
	}
	if cfg.SharedSecret == "" {
		return nil, fmt.Errorf("delegated auth: shared_secret is required")
	}
	if cfg.CallbackURL == "" {
		return nil, fmt.Errorf("delegated auth: callback_url is required")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	return &Provider{store: store, cfg: cfg}, nil
}

// NewFromConfig creates a delegated auth provider from the application config.
func NewFromConfig(store Store, dcfg config.DelegatedAuthConfig, sessionTTL time.Duration) (*Provider, error) {
	return New(store, Config{
		SettURL:      dcfg.SettURL,
		InstanceID:   dcfg.InstanceID,
		SharedSecret: dcfg.SharedSecret,
		CallbackURL:  dcfg.CallbackURL,
		SessionTTL:   sessionTTL,
	})
}

// jwtHeader represents the JWT header.
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// jwtClaims represents the claims in a Sett JWT assertion.
type jwtClaims struct {
	Sub         string     `json:"sub"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	Roles       []jwtRole  `json:"roles"`
	Exp         int64      `json:"exp"`
	Aud         string     `json:"aud"`
}

// jwtRole represents a group role assignment in the JWT.
type jwtRole struct {
	GroupID string `json:"group_id"`
	Role    string `json:"role"`
}

// Login returns a redirect URL to the Sett login page.
// Username/password are ignored for delegated auth.
func (p *Provider) Login(_ context.Context, _, _, _, _ string) (*authprovider.LoginResult, string, error) {
	redirectURL := fmt.Sprintf("%s/login?instance_id=%s&redirect_back=%s",
		strings.TrimRight(p.cfg.SettURL, "/"),
		url.QueryEscape(p.cfg.InstanceID),
		url.QueryEscape(p.cfg.CallbackURL),
	)
	return nil, redirectURL, nil
}

// HandleCallback validates the JWT assertion from Sett, upserts the user
// and their group roles, and creates a local session.
func (p *Provider) HandleCallback(ctx context.Context, r *http.Request) (*authprovider.LoginResult, error) {
	assertion := r.URL.Query().Get("assertion")
	if assertion == "" {
		return nil, fmt.Errorf("missing assertion parameter")
	}

	claims, err := p.validateJWT(assertion)
	if err != nil {
		return nil, fmt.Errorf("invalid assertion: %w", err)
	}

	// Upsert user: use Sett's UUID (sub) as the local user ID.
	user, err := p.upsertUser(ctx, claims)
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}

	// Sync group roles from JWT claims.
	if err := p.syncRoles(ctx, user.ID, claims.Roles); err != nil {
		return nil, fmt.Errorf("sync roles: %w", err)
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
// Sessions are stored locally, so this delegates to the local store.
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

// Capabilities reports that this provider delegates user management to Sett.
func (p *Provider) Capabilities() authprovider.ProviderCapabilities {
	return authprovider.ProviderCapabilities{
		CanManageUsers:     false,
		CanManagePasswords: false,
		CanChangePassword:  false,
		ManagedBy:          p.cfg.SettURL,
		LoginMode:          "redirect",
	}
}

// DeleteUserSessions deletes all sessions for a user. This is used by the
// managed API for active session revocation.
func (p *Provider) DeleteUserSessions(ctx context.Context, userID string) (int64, error) {
	return p.store.DeleteSessionsByUserID(ctx, userID)
}

// validateJWT parses and validates a Sett JWT assertion.
func (p *Provider) validateJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	// 1. Decode and validate header (check algorithm before computing signature).
	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if header.Alg != "HS256" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	// 2. Verify HMAC-SHA256 signature.
	signingInput := parts[0] + "." + parts[1]
	signature, err := base64URLDecode(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(p.cfg.SharedSecret))
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)
	if !hmac.Equal(signature, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	// 3. Decode and validate claims.
	claimsBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	// Validate expiration.
	if time.Now().UTC().Unix() >= claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	// Validate audience matches instance ID.
	if claims.Aud != p.cfg.InstanceID {
		return nil, fmt.Errorf("audience mismatch: got %q, want %q", claims.Aud, p.cfg.InstanceID)
	}

	// Validate required fields.
	if claims.Sub == "" {
		return nil, fmt.Errorf("missing sub claim")
	}
	if claims.Username == "" {
		return nil, fmt.Errorf("missing username claim")
	}

	return &claims, nil
}

// upsertUser creates or updates a user based on the JWT claims.
// Uses Sett's UUID (sub) as the local user ID for stable identity.
func (p *Provider) upsertUser(ctx context.Context, claims *jwtClaims) (*types.User, error) {
	now := time.Now().UTC()

	existing, err := p.store.GetUser(ctx, claims.Sub)
	if err != nil && !errors.Is(err, types.ErrUserNotFound) {
		return nil, fmt.Errorf("get user: %w", err)
	}

	if errors.Is(err, types.ErrUserNotFound) {
		// Create new user.
		displayName := claims.DisplayName
		if displayName == "" {
			displayName = claims.Username
		}
		user := types.User{
			ID:          claims.Sub,
			Username:    claims.Username,
			DisplayName: displayName,
			IsAdmin:     claims.IsAdmin,
			IsActive:    true,
			CreatedAt:   now,
			LastLoginAt: &now,
		}
		if err := p.store.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		return &user, nil
	}

	// Update existing user.
	existing.Username = claims.Username
	if claims.DisplayName != "" {
		existing.DisplayName = claims.DisplayName
	}
	existing.IsAdmin = claims.IsAdmin
	existing.IsActive = true
	existing.LastLoginAt = &now
	if err := p.store.UpdateUser(ctx, *existing); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	return existing, nil
}

// syncRoles clears existing group roles and applies the ones from the JWT.
func (p *Provider) syncRoles(ctx context.Context, userID string, roles []jwtRole) error {
	// Get current roles to know what to clear.
	existing, err := p.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return fmt.Errorf("list existing roles: %w", err)
	}

	// Delete all existing roles.
	for _, ugr := range existing {
		if err := p.store.DeleteUserGroupRole(ctx, userID, ugr.GroupID); err != nil {
			return fmt.Errorf("delete role for group %s: %w", ugr.GroupID, err)
		}
	}

	// Set new roles from JWT.
	for _, r := range roles {
		if !auth.IsDefaultRole(r.Role) {
			continue // skip unknown roles
		}
		if err := p.store.SetUserGroupRole(ctx, types.UserGroupRole{
			UserID:  userID,
			GroupID: r.GroupID,
			Role:    r.Role,
		}); err != nil {
			return fmt.Errorf("set role for group %s: %w", r.GroupID, err)
		}
	}
	return nil
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

// base64URLDecode decodes a base64url-encoded string (no padding).
func base64URLDecode(s string) ([]byte, error) {
	// Add padding if needed.
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}
