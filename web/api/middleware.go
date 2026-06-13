package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// contextKey is a private type for context keys in this package.
type contextKey int

const apiKeyCtxKey contextKey = iota

// APIKeyFromContext returns the API key stored in the request context.
func APIKeyFromContext(ctx context.Context) *types.APIKey {
	if v, ok := ctx.Value(apiKeyCtxKey).(*types.APIKey); ok {
		return v
	}
	return nil
}

// RequireAPIKey validates the Authorization: Bearer header and stores
// the API key in the request context.
func (a *API) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			writeError(w, http.StatusUnauthorized, "missing_key", "Authorization header is required")
			return
		}
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "invalid_header", "Authorization header must use Bearer scheme")
			return
		}

		key, err := a.keys.Validate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_key", "Invalid or expired API key")
			return
		}

		ctx := context.WithValue(r.Context(), apiKeyCtxKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireScope checks that the API key's scope grants the given permission.
func RequireScope(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := APIKeyFromContext(r.Context())
		if key == nil {
			writeError(w, http.StatusUnauthorized, "missing_key", "API key required")
			return
		}
		if !auth.Can(key.Scope, permission) {
			writeError(w, http.StatusForbidden, "forbidden",
				fmt.Sprintf("scope %q does not have permission %q", key.Scope, permission))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAgentScope checks scope permission AND that the API key is allowed
// to access the agent identified by the {id} path parameter.
func RequireAgentScope(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := APIKeyFromContext(r.Context())
		if key == nil {
			writeError(w, http.StatusUnauthorized, "missing_key", "API key required")
			return
		}
		if !auth.Can(key.Scope, permission) {
			writeError(w, http.StatusForbidden, "forbidden",
				fmt.Sprintf("scope %q does not have permission %q", key.Scope, permission))
			return
		}
		agentID := r.PathValue("id")
		if agentID != "" && !apikeys.CanAccessAgent(key, agentID) {
			writeError(w, http.StatusForbidden, "agent_scope",
				fmt.Sprintf("API key does not have access to agent %q", agentID))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimiter implements a per-key fixed-window rate limiter.
type RateLimiter struct {
	limits  RateLimits
	mu      sync.Mutex
	buckets map[string]*rateBucket
	stop    chan struct{}
}

type rateBucket struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter creates a rate limiter with the given scope-based limits.
func NewRateLimiter(limits RateLimits) *RateLimiter {
	rl := &RateLimiter{
		limits:  limits,
		buckets: make(map[string]*rateBucket),
		stop:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Stop terminates the cleanup goroutine.
func (rl *RateLimiter) Stop() {
	select {
	case <-rl.stop:
	default:
		close(rl.stop)
	}
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case now := <-ticker.C:
			rl.mu.Lock()
			for k, b := range rl.buckets {
				if now.After(b.resetAt) {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

func (rl *RateLimiter) limitForScope(scope string) int {
	switch scope {
	case auth.RoleAdmin:
		return rl.limits.Admin
	case auth.RoleManager:
		return rl.limits.Manager
	case auth.RoleOperator:
		return rl.limits.Operator
	default:
		return rl.limits.Viewer
	}
}

// Allow checks if the request is within rate limits for the given key.
func (rl *RateLimiter) Allow(keyID, scope string) (bool, int) {
	limit := rl.limitForScope(scope)
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[keyID]
	if !ok || now.After(b.resetAt) {
		rl.buckets[keyID] = &rateBucket{
			count:   1,
			resetAt: now.Add(time.Minute),
		}
		return true, 0
	}

	b.count++
	if b.count > limit {
		retryAfter := int(time.Until(b.resetAt).Seconds()) + 1
		return false, retryAfter
	}
	return true, 0
}

// RateLimit is the middleware that enforces per-key rate limiting.
func (a *API) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := APIKeyFromContext(r.Context())
		if key == nil {
			next.ServeHTTP(w, r)
			return
		}

		ok, retryAfter := a.limiter.Allow(key.ID, key.Scope)
		if !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			writeError(w, http.StatusTooManyRequests, "rate_limit", "Rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
