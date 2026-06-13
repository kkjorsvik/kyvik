package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// LoginPage renders the login form.
func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Error":    "",
		"Username": "",
	}
	if h.auth != nil {
		data["AuthCaps"] = h.auth.Capabilities()
	}
	h.renderFragment(w, r, "login", data)
}

// LoginSubmit validates credentials and sets a session cookie.
func (h *Handlers) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	lr, redirectURL, err := h.auth.Login(r.Context(), username, password, r.RemoteAddr, r.UserAgent())
	if err != nil {
		status := http.StatusUnauthorized
		if !errors.Is(err, types.ErrPermissionDenied) {
			status = http.StatusInternalServerError
		}
		data := map[string]any{
			"Error":    "Invalid username or password.",
			"Username": username,
		}
		if h.auth != nil {
			data["AuthCaps"] = h.auth.Capabilities()
		}
		w.WriteHeader(status)
		h.renderFragment(w, r, "login", data)
		return
	}

	if redirectURL != "" {
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}

	maxAge := int(h.auth.SessionTTL().Seconds())
	if maxAge <= 0 {
		maxAge = 86400
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    lr.SessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})

	if lr.ForcePasswordChange {
		http.Redirect(w, r, "/password/change", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout clears the session cookie.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if h.auth != nil {
		if cookie, err := r.Cookie(cookieName); err == nil {
			_ = h.auth.Logout(r.Context(), cookie.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// AuthRedirect initiates the external login flow via a GET request.
// Using GET instead of form POST avoids CSP form-action restrictions on the
// redirect chain to external auth providers.
func (h *Handlers) AuthRedirect(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	_, redirectURL, err := h.auth.Login(r.Context(), "", "", r.RemoteAddr, r.UserAgent())
	if err != nil || redirectURL == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// AuthCallback handles the redirect-back from external auth providers (OIDC, delegated).
func (h *Handlers) AuthCallback(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	lr, err := h.auth.HandleCallback(r.Context(), r)
	if err != nil {
		if errors.Is(err, authprovider.ErrNotSupported) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "Authentication failed", http.StatusUnauthorized)
		return
	}

	maxAge := int(h.auth.SessionTTL().Seconds())
	if maxAge <= 0 {
		maxAge = 86400
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    lr.SessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode, // Lax needed for cross-origin redirect-back
		MaxAge:   maxAge,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// PasswordChangePage renders the first-login password update page.
func (h *Handlers) PasswordChangePage(w http.ResponseWriter, r *http.Request) {
	if h.auth != nil && !h.auth.Capabilities().CanChangePassword {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u, ok := currentDashboardUser(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.renderFragment(w, r, "password-change", map[string]any{
		"Username": u.Username,
		"Error":    "",
	})
}

// PasswordChangeSubmit sets a new password and clears force_password_change.
func (h *Handlers) PasswordChangeSubmit(w http.ResponseWriter, r *http.Request) {
	if h.auth != nil && !h.auth.Capabilities().CanChangePassword {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if h.userSvc == nil {
		http.Error(w, "not supported", http.StatusNotFound)
		return
	}
	u, ok := currentDashboardUser(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")
	if len(strings.TrimSpace(newPassword)) < 10 {
		w.WriteHeader(http.StatusBadRequest)
		h.renderFragment(w, r, "password-change", map[string]any{
			"Username": u.Username,
			"Error":    "Password must be at least 10 characters.",
		})
		return
	}
	if newPassword != confirmPassword {
		w.WriteHeader(http.StatusBadRequest)
		h.renderFragment(w, r, "password-change", map[string]any{
			"Username": u.Username,
			"Error":    "Passwords do not match.",
		})
		return
	}

	if err := h.userSvc.UpdatePassword(r.Context(), u.ID, newPassword); err != nil {
		http.Error(w, "failed to update password", http.StatusInternalServerError)
		return
	}
	_ = h.userSvc.DeleteBootstrapCredsFile()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
