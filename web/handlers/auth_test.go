package handlers

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockAuthProvider implements authprovider.AuthProvider for testing.
type mockAuthProvider struct {
	loginResult    *authprovider.LoginResult
	loginRedirect  string
	loginErr       error
	callbackResult *authprovider.LoginResult
	callbackErr    error
	validateUser   *types.User
	validateErr    error
	logoutErr      error
	sessionTTL     time.Duration
	globalRole     string
	globalRoleOK   bool
	globalRoleErr  error
	agentRole      string
	agentRoleOK    bool
	agentRoleErr   error
	capabilities   authprovider.ProviderCapabilities
}

func (m *mockAuthProvider) Login(_ context.Context, _, _, _, _ string) (*authprovider.LoginResult, string, error) {
	return m.loginResult, m.loginRedirect, m.loginErr
}

func (m *mockAuthProvider) HandleCallback(_ context.Context, _ *http.Request) (*authprovider.LoginResult, error) {
	return m.callbackResult, m.callbackErr
}

func (m *mockAuthProvider) ValidateSession(_ context.Context, _ string) (*types.User, error) {
	return m.validateUser, m.validateErr
}

func (m *mockAuthProvider) Logout(_ context.Context, _ string) error {
	return m.logoutErr
}

func (m *mockAuthProvider) SessionTTL() time.Duration {
	if m.sessionTTL == 0 {
		return time.Hour
	}
	return m.sessionTTL
}

func (m *mockAuthProvider) ResolveGlobalRole(_ context.Context, _ string) (string, bool, error) {
	return m.globalRole, m.globalRoleOK, m.globalRoleErr
}

func (m *mockAuthProvider) ResolveAgentRole(_ context.Context, _, _ string) (string, bool, error) {
	return m.agentRole, m.agentRoleOK, m.agentRoleErr
}

func (m *mockAuthProvider) ListVisibleAgentIDs(_ context.Context, _ string) (map[string]struct{}, error) {
	return nil, nil
}

func (m *mockAuthProvider) Capabilities() authprovider.ProviderCapabilities {
	return m.capabilities
}

// testTemplates creates minimal template stubs for use in auth handler tests.
func testTemplates(t *testing.T) *template.Template {
	t.Helper()
	const tmplSrc = `
{{define "login"}}login:{{if .Error}}error={{.Error}}{{end}}{{end}}
{{define "layout"}}layout:{{.Content}}{{end}}
{{define "password-change"}}password-change:{{if .Error}}error={{.Error}}{{end}}{{end}}
`
	tmpl, err := template.New("test").Parse(tmplSrc)
	if err != nil {
		t.Fatalf("testTemplates: parse: %v", err)
	}
	return tmpl
}

// newAuthTestHandlers returns a minimal Handlers wired with the given auth provider and templates.
func newAuthTestHandlers(auth authprovider.AuthProvider, tmpl *template.Template) *Handlers {
	return &Handlers{
		sessionKey: generateKey(),
		auth:       auth,
		templates:  tmpl,
	}
}

func TestLoginSubmitValidCredentials(t *testing.T) {
	mock := &mockAuthProvider{
		loginResult: &authprovider.LoginResult{
			SessionID: "sess-abc",
			UserID:    "u-1",
			Username:  "alice",
		},
		sessionTTL: time.Hour,
	}
	h := newAuthTestHandlers(mock, testTemplates(t))

	form := url.Values{}
	form.Set("username", "alice")
	form.Set("password", "correct-horse-battery")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.LoginSubmit(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("expected redirect to /, got %q", loc)
	}

	// Verify session cookie is set.
	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set")
	}
	if sessionCookie.Value != "sess-abc" {
		t.Errorf("expected session cookie value %q, got %q", "sess-abc", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly {
		t.Error("expected HttpOnly cookie")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", sessionCookie.SameSite)
	}
}

func TestLoginSubmitInvalidCredentials(t *testing.T) {
	mock := &mockAuthProvider{
		loginErr: types.ErrPermissionDenied,
	}
	h := newAuthTestHandlers(mock, testTemplates(t))

	form := url.Values{}
	form.Set("username", "alice")
	form.Set("password", "wrong")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.LoginSubmit(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Invalid username or password") {
		t.Errorf("expected error message in body, got: %q", body)
	}
}

func TestLoginSubmitInternalError(t *testing.T) {
	mock := &mockAuthProvider{
		loginErr: errors.New("db connection lost"),
	}
	h := newAuthTestHandlers(mock, testTemplates(t))

	form := url.Values{}
	form.Set("username", "alice")
	form.Set("password", "whatever")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.LoginSubmit(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-permission error, got %d", rec.Code)
	}
}

func TestLoginSubmitForcePasswordChange(t *testing.T) {
	mock := &mockAuthProvider{
		loginResult: &authprovider.LoginResult{
			SessionID:           "sess-forced",
			UserID:              "u-2",
			Username:            "bob",
			ForcePasswordChange: true,
		},
		sessionTTL: time.Hour,
	}
	h := newAuthTestHandlers(mock, testTemplates(t))

	form := url.Values{}
	form.Set("username", "bob")
	form.Set("password", "pass")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.LoginSubmit(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/password/change" {
		t.Errorf("expected redirect to /password/change, got %q", loc)
	}
}

func TestLogoutCookieHasSameSite(t *testing.T) {
	mock := &mockAuthProvider{}
	h := newAuthTestHandlers(mock, nil)

	// Add a session cookie so Logout can call auth.Logout.
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "some-session"})
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}

	cookies := rec.Result().Cookies()
	var cleared *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			cleared = c
			break
		}
	}
	if cleared == nil {
		t.Fatal("expected cookie to be cleared in response")
	}
	if cleared.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", cleared.SameSite)
	}
	if cleared.MaxAge != -1 {
		t.Errorf("expected MaxAge=-1, got %d", cleared.MaxAge)
	}
	if cleared.Value != "" {
		t.Errorf("expected empty cookie value, got %q", cleared.Value)
	}
}

func TestRequireAuthValidSession(t *testing.T) {
	mock := &mockAuthProvider{
		validateUser: &types.User{
			ID:       "u-1",
			Username: "alice",
			IsAdmin:  true,
		},
	}
	h := newAuthTestHandlers(mock, nil)

	var capturedUser dashboardUser
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := currentDashboardUser(r.Context())
		if !ok {
			t.Error("expected user in context")
		}
		capturedUser = u
		w.WriteHeader(http.StatusOK)
	})

	handler := h.RequireAuth(protected)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "valid-session"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser.Username != "alice" {
		t.Errorf("expected username alice, got %q", capturedUser.Username)
	}
	if !capturedUser.IsAdmin {
		t.Error("expected IsAdmin=true")
	}
}

func TestRequireAuthMissingCookie(t *testing.T) {
	h := newAuthTestHandlers(&mockAuthProvider{}, nil)

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("protected handler should not be called when cookie is missing")
		w.WriteHeader(http.StatusOK)
	})

	handler := h.RequireAuth(protected)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestRequireAuthHTMXRedirect(t *testing.T) {
	h := newAuthTestHandlers(&mockAuthProvider{}, nil)

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("protected handler should not be called for HTMX redirect")
		w.WriteHeader(http.StatusOK)
	})

	handler := h.RequireAuth(protected)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for HTMX, got %d", rec.Code)
	}
	if hxr := rec.Header().Get("HX-Redirect"); hxr != "/login" {
		t.Errorf("expected HX-Redirect=/login, got %q", hxr)
	}
}

func TestAuthCallbackSuccess(t *testing.T) {
	mock := &mockAuthProvider{
		callbackResult: &authprovider.LoginResult{
			SessionID: "sess-callback",
			UserID:    "u-ext",
			Username:  "external-user",
		},
		sessionTTL: time.Hour,
	}
	h := newAuthTestHandlers(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=xyz", nil)
	rec := httptest.NewRecorder()

	h.AuthCallback(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("expected redirect to /, got %q", loc)
	}

	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set after callback")
	}
	if sessionCookie.Value != "sess-callback" {
		t.Errorf("expected session ID %q, got %q", "sess-callback", sessionCookie.Value)
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", sessionCookie.SameSite)
	}
}

func TestAuthCallbackFailure(t *testing.T) {
	mock := &mockAuthProvider{
		callbackErr: errors.New("invalid token"),
	}
	h := newAuthTestHandlers(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=bad", nil)
	rec := httptest.NewRecorder()

	h.AuthCallback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthCallbackNotSupported(t *testing.T) {
	mock := &mockAuthProvider{
		callbackErr: authprovider.ErrNotSupported,
	}
	h := newAuthTestHandlers(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	rec := httptest.NewRecorder()

	h.AuthCallback(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect for unsupported, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestSameOriginRequest(t *testing.T) {
	h := &Handlers{sessionKey: generateKey()}

	cases := []struct {
		name    string
		method  string
		host    string
		origin  string
		referer string
		want    bool
	}{
		{
			name:   "GET always passes",
			method: http.MethodGet,
			host:   "kyvik.local",
			origin: "https://evil.example",
			want:   true,
		},
		{
			name:   "POST no origin no referer passes",
			method: http.MethodPost,
			host:   "kyvik.local",
			want:   true,
		},
		{
			name:    "POST matching origin passes",
			method:  http.MethodPost,
			host:    "kyvik.local",
			origin:  "https://kyvik.local",
			want:    true,
		},
		{
			name:    "POST mismatched origin fails",
			method:  http.MethodPost,
			host:    "kyvik.local",
			origin:  "https://evil.example",
			want:    false,
		},
		{
			name:    "POST matching referer passes",
			method:  http.MethodPost,
			host:    "kyvik.local",
			referer: "https://kyvik.local/page",
			want:    true,
		},
		{
			name:    "POST mismatched referer fails",
			method:  http.MethodPost,
			host:    "kyvik.local",
			referer: "https://evil.example/page",
			want:    false,
		},
		{
			name:    "DELETE matching origin passes",
			method:  http.MethodDelete,
			host:    "kyvik.local",
			origin:  "https://kyvik.local",
			want:    true,
		},
		{
			name:    "DELETE mismatched origin fails",
			method:  http.MethodDelete,
			host:    "kyvik.local",
			origin:  "http://attacker.example",
			want:    false,
		},
		{
			name:    "PUT matching both passes",
			method:  http.MethodPut,
			host:    "kyvik.local",
			origin:  "https://kyvik.local",
			referer: "https://kyvik.local/settings",
			want:    true,
		},
		{
			name:    "PATCH origin matches but referer mismatches fails",
			method:  http.MethodPatch,
			host:    "kyvik.local",
			origin:  "https://kyvik.local",
			referer: "https://evil.example/inject",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			got := h.sameOriginRequest(req)
			if got != tc.want {
				t.Errorf("sameOriginRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}
