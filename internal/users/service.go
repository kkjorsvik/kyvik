package users

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Store is the minimal persistence contract required by Service.
type Store interface {
	CreateUser(ctx context.Context, user types.User) error
	GetUser(ctx context.Context, id string) (*types.User, error)
	GetUserByUsername(ctx context.Context, username string) (*types.User, error)
	ListUsers(ctx context.Context) ([]types.User, error)
	UpdateUser(ctx context.Context, user types.User) error
	DeleteUser(ctx context.Context, id string) error
	ListUserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error)
	SetUserGroupRole(ctx context.Context, ugr types.UserGroupRole) error
	DeleteUserGroupRole(ctx context.Context, userID, groupID string) error
	ListGroupIDsByAgent(ctx context.Context, agentID string) ([]string, error)
	ListAgentIDsByGroup(ctx context.Context, groupID string) ([]string, error)
	CreateAgentGroup(ctx context.Context, g types.AgentGroup) error
	GetAgentGroup(ctx context.Context, id string) (*types.AgentGroup, error)
	ListAgentGroups(ctx context.Context) ([]types.AgentGroup, error)
	UpdateAgentGroup(ctx context.Context, g types.AgentGroup) error
	DeleteAgentGroup(ctx context.Context, id string) error
	SetAgentGroupMember(ctx context.Context, agentID, groupID string) error
	RemoveAgentGroupMember(ctx context.Context, agentID, groupID string) error

	CreateSession(ctx context.Context, sess types.UserSession) error
	GetSession(ctx context.Context, id string) (*types.UserSession, error)
	DeleteSession(ctx context.Context, id string) error
	UpdateSessionLastSeen(ctx context.Context, id string, at time.Time) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
	DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error)
	EnforceSessionLimit(ctx context.Context, userID string, maxSessions int) (int64, error)
}

// AuthConfig controls session/auth behavior.
type AuthConfig struct {
	SessionTTL         time.Duration
	MaxSessionsPerUser int
	BootstrapCredsPath string
}

// Service manages users and web dashboard sessions.
type Service struct {
	store Store
	cfg   AuthConfig
}

// LoginResult contains the created session and user metadata.
type LoginResult struct {
	SessionID           string
	UserID              string
	Username            string
	ForcePasswordChange bool
	ExpiresAt           time.Time
}

type CreateUserParams struct {
	Username    string
	Password    string
	DisplayName string
	IsAdmin     bool
}

// New creates a users service.
func New(store Store, cfg AuthConfig) *Service {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = 3
	}
	return &Service{store: store, cfg: cfg}
}

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration { return s.cfg.SessionTTL }

// Authenticate validates credentials and creates a session.
func (s *Service) Authenticate(ctx context.Context, username, password, ip, userAgent string) (*LoginResult, error) {
	if err := s.cleanupSessions(ctx); err != nil {
		return nil, err
	}

	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, types.ErrUserNotFound) {
			return nil, types.ErrPermissionDenied
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if !user.IsActive {
		return nil, types.ErrPermissionDenied
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, types.ErrPermissionDenied
	}

	now := time.Now().UTC()
	exp := now.Add(s.cfg.SessionTTL)
	sess := types.UserSession{
		ID:         uuid.NewString(),
		UserID:     user.ID,
		CreatedAt:  now,
		ExpiresAt:  exp,
		LastSeenAt: &now,
		IPAddress:  ip,
		UserAgent:  userAgent,
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	user.LastLoginAt = &now
	if err := s.store.UpdateUser(ctx, *user); err != nil {
		return nil, fmt.Errorf("update last_login_at: %w", err)
	}

	if _, err := s.store.EnforceSessionLimit(ctx, user.ID, s.cfg.MaxSessionsPerUser); err != nil {
		return nil, fmt.Errorf("enforce session limit: %w", err)
	}

	return &LoginResult{
		SessionID:           sess.ID,
		UserID:              user.ID,
		Username:            user.Username,
		ForcePasswordChange: user.ForcePasswordChange,
		ExpiresAt:           exp,
	}, nil
}

// ValidateSession verifies session validity and returns the owning user.
func (s *Service) ValidateSession(ctx context.Context, sessionID string) (*types.User, error) {
	if sessionID == "" {
		return nil, types.ErrSessionNotFound
	}

	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(now) {
		_ = s.store.DeleteSession(ctx, sessionID)
		return nil, types.ErrSessionNotFound
	}
	if sess.LastSeenAt == nil || now.Sub(*sess.LastSeenAt) >= time.Minute {
		_ = s.store.UpdateSessionLastSeen(ctx, sessionID, now)
	}

	user, err := s.store.GetUser(ctx, sess.UserID)
	if err != nil {
		_ = s.store.DeleteSession(ctx, sessionID)
		return nil, err
	}
	if !user.IsActive {
		_ = s.store.DeleteSession(ctx, sessionID)
		return nil, types.ErrPermissionDenied
	}
	return user, nil
}

// Logout deletes a session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, sessionID); err != nil && !errors.Is(err, types.ErrSessionNotFound) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ResolveGlobalRole returns the highest default role granted to the user
// across all of their user_group_roles records.
func (s *Service) ResolveGlobalRole(ctx context.Context, userID string) (string, bool, error) {
	roles, err := s.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return "", false, fmt.Errorf("list user group roles: %w", err)
	}
	top := highestDefaultRole(userGroupRoleNames(roles))
	if top == "" {
		return "", false, nil
	}
	return top, true, nil
}

// ResolveAgentRole returns the highest role for this user on this specific agent,
// based on overlapping user group memberships and agent group memberships.
func (s *Service) ResolveAgentRole(ctx context.Context, userID, agentID string) (string, bool, error) {
	userRoles, err := s.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return "", false, fmt.Errorf("list user group roles: %w", err)
	}
	agentGroupIDs, err := s.store.ListGroupIDsByAgent(ctx, agentID)
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
	var matching []string
	for _, ugr := range userRoles {
		if _, ok := groupSet[ugr.GroupID]; ok {
			matching = append(matching, ugr.Role)
		}
	}
	top := highestDefaultRole(matching)
	if top == "" {
		return "", false, nil
	}
	return top, true, nil
}

// ListVisibleAgentIDs returns all agent IDs visible to the user via group membership.
func (s *Service) ListVisibleAgentIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	userRoles, err := s.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user group roles: %w", err)
	}
	agentIDs := make(map[string]struct{})
	for _, ugr := range userRoles {
		ids, listErr := s.store.ListAgentIDsByGroup(ctx, ugr.GroupID)
		if listErr != nil {
			return nil, fmt.Errorf("list agents by group %s: %w", ugr.GroupID, listErr)
		}
		for _, id := range ids {
			agentIDs[id] = struct{}{}
		}
	}
	return agentIDs, nil
}

// UpdatePassword updates a user's password hash and clears force_password_change.
func (s *Service) UpdatePassword(ctx context.Context, userID, newPassword string) error {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user.PasswordHash = string(hash)
	user.ForcePasswordChange = false
	if err := s.store.UpdateUser(ctx, *user); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// ListUsers returns all dashboard users.
func (s *Service) ListUsers(ctx context.Context) ([]types.User, error) {
	return s.store.ListUsers(ctx)
}

// CreateUser creates a new dashboard user.
func (s *Service) CreateUser(ctx context.Context, p CreateUserParams) (*types.User, error) {
	username := strings.TrimSpace(p.Username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	password := strings.TrimSpace(p.Password)
	if len(password) < 10 {
		return nil, fmt.Errorf("password must be at least 10 characters")
	}
	displayName := strings.TrimSpace(p.DisplayName)
	if displayName == "" {
		displayName = username
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	u := types.User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: string(hash),
		DisplayName:  displayName,
		IsAdmin:      p.IsAdmin,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

// UpdateUserProfile updates mutable user profile fields.
func (s *Service) UpdateUserProfile(ctx context.Context, userID, displayName string, active bool) error {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	u.DisplayName = strings.TrimSpace(displayName)
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	u.IsActive = active
	if err := s.store.UpdateUser(ctx, *u); err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}
	return nil
}

// ResetPassword replaces a user's password and forces a password change on next login.
func (s *Service) ResetPassword(ctx context.Context, userID, newPassword string) error {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(newPassword)) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	u.PasswordHash = string(hash)
	u.ForcePasswordChange = true
	if err := s.store.UpdateUser(ctx, *u); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// DeleteUser removes a user account.
func (s *Service) DeleteUser(ctx context.Context, userID string) error {
	return s.store.DeleteUser(ctx, userID)
}

// ListGroups returns all agent groups.
func (s *Service) ListGroups(ctx context.Context) ([]types.AgentGroup, error) {
	return s.store.ListAgentGroups(ctx)
}

// CreateGroup creates an agent group.
func (s *Service) CreateGroup(ctx context.Context, name, description string) (*types.AgentGroup, error) {
	g := types.AgentGroup{
		ID:          uuid.NewString(),
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		CreatedAt:   time.Now().UTC(),
	}
	if g.Name == "" {
		return nil, fmt.Errorf("group name is required")
	}
	if err := s.store.CreateAgentGroup(ctx, g); err != nil {
		return nil, fmt.Errorf("create group: %w", err)
	}
	return &g, nil
}

// UpdateGroup updates group metadata.
func (s *Service) UpdateGroup(ctx context.Context, groupID, name, description string) error {
	g, err := s.store.GetAgentGroup(ctx, groupID)
	if err != nil {
		return err
	}
	g.Name = strings.TrimSpace(name)
	g.Description = strings.TrimSpace(description)
	if g.Name == "" {
		return fmt.Errorf("group name is required")
	}
	if err := s.store.UpdateAgentGroup(ctx, *g); err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	return nil
}

// DeleteGroup removes an agent group.
func (s *Service) DeleteGroup(ctx context.Context, groupID string) error {
	return s.store.DeleteAgentGroup(ctx, groupID)
}

// AddAgentToGroup attaches an agent to a group.
func (s *Service) AddAgentToGroup(ctx context.Context, groupID, agentID string) error {
	return s.store.SetAgentGroupMember(ctx, agentID, groupID)
}

// RemoveAgentFromGroup detaches an agent from a group.
func (s *Service) RemoveAgentFromGroup(ctx context.Context, groupID, agentID string) error {
	return s.store.RemoveAgentGroupMember(ctx, agentID, groupID)
}

// SetUserRoleInGroup assigns or updates user role for a group.
func (s *Service) SetUserRoleInGroup(ctx context.Context, userID, groupID, role string) error {
	role = strings.TrimSpace(role)
	if !auth.IsDefaultRole(role) {
		return fmt.Errorf("invalid role")
	}
	return s.store.SetUserGroupRole(ctx, types.UserGroupRole{
		UserID:  userID,
		GroupID: groupID,
		Role:    role,
	})
}

// RemoveUserRoleInGroup removes group role assignment.
func (s *Service) RemoveUserRoleInGroup(ctx context.Context, userID, groupID string) error {
	return s.store.DeleteUserGroupRole(ctx, userID, groupID)
}

// UserGroupRoles returns group-role assignments for a user.
func (s *Service) UserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error) {
	return s.store.ListUserGroupRoles(ctx, userID)
}

// GroupAgentIDs returns agent IDs in a group.
func (s *Service) GroupAgentIDs(ctx context.Context, groupID string) ([]string, error) {
	return s.store.ListAgentIDsByGroup(ctx, groupID)
}

// DeleteBootstrapCredsFile removes the bootstrap credentials file when configured.
func (s *Service) DeleteBootstrapCredsFile() error {
	if strings.TrimSpace(s.cfg.BootstrapCredsPath) == "" {
		return nil
	}
	err := os.Remove(s.cfg.BootstrapCredsPath)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// BootstrapAdminIfEmpty creates an initial admin user when users table is empty.
// If legacyPassword is provided, it is used and no credentials file is written.
// If legacyPassword is empty, a random password is generated and written to cfg.BootstrapCredsPath.
func (s *Service) BootstrapAdminIfEmpty(ctx context.Context, legacyUsername, legacyPassword string) (created bool, generatedPassword string, err error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return false, "", fmt.Errorf("list users: %w", err)
	}
	if len(users) > 0 {
		return false, "", nil
	}

	username := strings.TrimSpace(legacyUsername)
	if username == "" {
		username = "admin"
	}
	password := legacyPassword
	forceChange := false
	if strings.TrimSpace(password) == "" {
		password, err = randomPassword(16)
		if err != nil {
			return false, "", fmt.Errorf("generate bootstrap password: %w", err)
		}
		forceChange = true
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, "", fmt.Errorf("hash bootstrap password: %w", err)
	}

	user := types.User{
		ID:                  uuid.NewString(),
		Username:            username,
		PasswordHash:        string(hash),
		DisplayName:         "Administrator",
		IsAdmin:             true,
		IsActive:            true,
		CreatedAt:           time.Now().UTC(),
		ForcePasswordChange: forceChange,
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return false, "", fmt.Errorf("create bootstrap admin: %w", err)
	}

	if forceChange && s.cfg.BootstrapCredsPath != "" {
		dir := filepath.Dir(s.cfg.BootstrapCredsPath)
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return true, password, fmt.Errorf("create credentials dir: %w", mkErr)
		}
		content := fmt.Sprintf("username=%s\npassword=%s\n", username, password)
		if writeErr := os.WriteFile(s.cfg.BootstrapCredsPath, []byte(content), 0o600); writeErr != nil {
			return true, password, fmt.Errorf("write bootstrap credentials: %w", writeErr)
		}
	}

	if forceChange {
		return true, password, nil
	}
	return true, "", nil
}

func (s *Service) cleanupSessions(ctx context.Context) error {
	_, err := s.store.DeleteExpiredSessions(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if n <= 0 {
		return "", nil
	}
	var b strings.Builder
	b.Grow(n)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[v.Int64()])
	}
	return b.String(), nil
}

func userGroupRoleNames(roles []types.UserGroupRole) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Role)
	}
	return out
}

func highestDefaultRole(roles []string) string {
	top := ""
	for _, role := range roles {
		if !auth.IsDefaultRole(role) {
			continue
		}
		if top == "" {
			top = role
			continue
		}
		top = auth.MorePermissiveRole(top, role)
	}
	return top
}
