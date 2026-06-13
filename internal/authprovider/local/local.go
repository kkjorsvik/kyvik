package local

import (
	"context"
	"net/http"
	"time"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Provider wraps *users.Service as an AuthProvider for local (DB-backed) auth.
type Provider struct {
	svc *users.Service
}

// New creates a local auth provider wrapping the given users service.
func New(svc *users.Service) *Provider {
	return &Provider{svc: svc}
}

// UserService exposes the underlying service for user CRUD operations.
func (p *Provider) UserService() *users.Service {
	return p.svc
}

func (p *Provider) Login(ctx context.Context, username, password, ip, userAgent string) (*authprovider.LoginResult, string, error) {
	lr, err := p.svc.Authenticate(ctx, username, password, ip, userAgent)
	if err != nil {
		return nil, "", err
	}
	return &authprovider.LoginResult{
		SessionID:           lr.SessionID,
		UserID:              lr.UserID,
		Username:            lr.Username,
		ForcePasswordChange: lr.ForcePasswordChange,
		ExpiresAt:           lr.ExpiresAt,
	}, "", nil
}

func (p *Provider) HandleCallback(_ context.Context, _ *http.Request) (*authprovider.LoginResult, error) {
	return nil, authprovider.ErrNotSupported
}

func (p *Provider) ValidateSession(ctx context.Context, sessionID string) (*types.User, error) {
	return p.svc.ValidateSession(ctx, sessionID)
}

func (p *Provider) Logout(ctx context.Context, sessionID string) error {
	return p.svc.Logout(ctx, sessionID)
}

func (p *Provider) SessionTTL() time.Duration {
	return p.svc.SessionTTL()
}

func (p *Provider) ResolveGlobalRole(ctx context.Context, userID string) (string, bool, error) {
	return p.svc.ResolveGlobalRole(ctx, userID)
}

func (p *Provider) ResolveAgentRole(ctx context.Context, userID, agentID string) (string, bool, error) {
	return p.svc.ResolveAgentRole(ctx, userID, agentID)
}

func (p *Provider) ListVisibleAgentIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	return p.svc.ListVisibleAgentIDs(ctx, userID)
}

func (p *Provider) Capabilities() authprovider.ProviderCapabilities {
	return authprovider.ProviderCapabilities{
		CanManageUsers:     true,
		CanManagePasswords: true,
		CanChangePassword:  true,
		LoginMode:          "form",
	}
}
