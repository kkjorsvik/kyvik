package authprovider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AuthProvider abstracts authentication and authorization.
type AuthProvider interface {
	Login(ctx context.Context, username, password, ip, userAgent string) (result *LoginResult, redirectURL string, err error)
	HandleCallback(ctx context.Context, r *http.Request) (*LoginResult, error)
	ValidateSession(ctx context.Context, sessionID string) (*types.User, error)
	Logout(ctx context.Context, sessionID string) error
	SessionTTL() time.Duration
	ResolveGlobalRole(ctx context.Context, userID string) (role string, found bool, err error)
	ResolveAgentRole(ctx context.Context, userID, agentID string) (role string, found bool, err error)
	ListVisibleAgentIDs(ctx context.Context, userID string) (map[string]struct{}, error)
	Capabilities() ProviderCapabilities
}

// LoginResult contains the created session and user metadata.
type LoginResult struct {
	SessionID           string
	UserID              string
	Username            string
	ForcePasswordChange bool
	ExpiresAt           time.Time
}

// ProviderCapabilities describes what a provider can do.
type ProviderCapabilities struct {
	CanManageUsers     bool
	CanManagePasswords bool
	CanChangePassword  bool
	ManagedBy          string // non-empty = show "managed by X" banner
	LoginMode          string // "form" or "redirect"
}

// ErrNotSupported is returned when an operation is not supported by the provider.
var ErrNotSupported = errors.New("operation not supported by this auth provider")
