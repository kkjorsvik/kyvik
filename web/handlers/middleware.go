package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/auth"
)

type requestIDKey struct{}

// RequestID is middleware that generates a random 16-hex-char request ID
// and stores it in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			panic("failed to generate request ID: " + err.Error())
		}
		id := hex.EncodeToString(b)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDFromContext retrieves the request ID from the context.
func requestIDFromContext(ctx context.Context) string {
	v := ctx.Value(requestIDKey{})
	if v == nil {
		return ""
	}
	id, _ := v.(string)
	return id
}

const cookieName = "kyvik_session"

type userCtxKey struct{}

type dashboardUser struct {
	ID                  string
	Username            string
	Role                string
	IsAdmin             bool
	ForcePasswordChange bool
}

// generateKey creates a 32-byte random key for HMAC signing.
func generateKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("failed to generate session key: " + err.Error())
	}
	return key
}

// RequireAuth is middleware that checks for a valid session cookie.
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		valid := false
		var principal dashboardUser
		if err == nil && h.auth != nil {
			if u, verr := h.auth.ValidateSession(r.Context(), cookie.Value); verr == nil {
				role := ""
				if !u.IsAdmin {
					if rr, ok, rerr := h.auth.ResolveGlobalRole(r.Context(), u.ID); rerr == nil && ok {
						role = rr
					}
				}
				valid = true
				principal = dashboardUser{
					ID:                  u.ID,
					Username:            u.Username,
					IsAdmin:             u.IsAdmin,
					Role:                roleForDashboardUser(u.IsAdmin, role),
					ForcePasswordChange: u.ForcePasswordChange,
				}
			}
		}
		if !valid {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if principal.ForcePasswordChange && r.URL.Path != "/password/change" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/password/change", http.StatusSeeOther)
			return
		}
		if !h.sameOriginRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, principal)))
	})
}

// RequirePermission denies requests when the authenticated user lacks permission.
func (h *Handlers) RequirePermission(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil {
			// No auth provider configured — allow unrestricted access.
			next.ServeHTTP(w, r)
			return
		}

		u, ok := currentDashboardUser(r.Context())
		if !ok {
			h.handleAuthRedirect(w, r)
			return
		}
		if u.IsAdmin || auth.Can(u.Role, permission) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// RequireAgentPermission enforces group-scoped role resolution for agent routes.
func (h *Handlers) RequireAgentPermission(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		u, ok := currentDashboardUser(r.Context())
		if !ok {
			h.handleAuthRedirect(w, r)
			return
		}
		if u.IsAdmin {
			if auth.Can(auth.RoleAdmin, permission) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		agentID := r.PathValue("id")
		if strings.TrimSpace(agentID) == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		role, found, err := h.auth.ResolveAgentRole(r.Context(), u.ID, agentID)
		if err != nil {
			http.Error(w, "authorization error", http.StatusInternalServerError)
			return
		}
		if !found || !auth.Can(role, permission) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) handleAuthRedirect(w http.ResponseWriter, r *http.Request) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// sign produces an HMAC-SHA256 signature, hex-encoded.
func (h *Handlers) sign(payload string) string {
	mac := hmac.New(sha256.New, h.sessionKey)
	mac.Write([]byte(payload))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func roleForDashboardUser(isAdmin bool, resolvedRole string) string {
	if isAdmin {
		return auth.RoleAdmin
	}
	return resolvedRole
}

func currentDashboardUser(ctx context.Context) (dashboardUser, bool) {
	v := ctx.Value(userCtxKey{})
	if v == nil {
		return dashboardUser{}, false
	}
	u, ok := v.(dashboardUser)
	return u, ok
}

func (h *Handlers) isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if len(h.trustedProxies) == 0 {
		return false
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if !h.trustedProxies[ip] {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (h *Handlers) sameOriginRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return true
	}

	expectedHost := h.requestHost(r)
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	referer := strings.TrimSpace(r.Header.Get("Referer"))

	if origin == "" && referer == "" {
		// Some clients omit both headers; keep compatibility.
		return true
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !hostMatches(expectedHost, u.Host) {
			return false
		}
	}
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || !hostMatches(expectedHost, u.Host) {
			return false
		}
	}
	return true
}

func (h *Handlers) requestHost(r *http.Request) string {
	if len(h.trustedProxies) > 0 {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}
		if h.trustedProxies[ip] {
			if host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); host != "" {
				if i := strings.Index(host, ","); i >= 0 {
					host = host[:i]
				}
				return strings.ToLower(strings.TrimSpace(host))
			}
		}
	}
	return strings.ToLower(strings.TrimSpace(r.Host))
}

func hostMatches(expected, actual string) bool {
	return strings.EqualFold(strings.TrimSpace(expected), strings.TrimSpace(actual))
}

// RequireFormContentType is middleware that rejects POST/PUT/PATCH requests
// without a valid form Content-Type header.
// NOTE: This middleware is NOT applied globally because AgentMemoriesImport
// (POST /agents/{id}/memories/import) accepts application/json request bodies
// for bulk memory imports. To use globally, that route would need refactoring.
func RequireFormContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") &&
				!strings.HasPrefix(ct, "multipart/form-data") {
				http.Error(w, "Unsupported content type", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
