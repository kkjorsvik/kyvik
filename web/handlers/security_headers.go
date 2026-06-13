package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// cspNonceKey is the context key type for the per-request CSP nonce.
type cspNonceKey struct{}

// generateCSPNonce returns a base64-encoded 16-byte random nonce for use in
// Content-Security-Policy script-src directives.
func generateCSPNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "NONCE_ERROR"
	}
	return base64.StdEncoding.EncodeToString(b)
}

// cspNonceFromContext retrieves the per-request CSP nonce stored by the
// SecurityHeaders middleware. Returns an empty string if not set.
func cspNonceFromContext(ctx context.Context) string {
	if n, ok := ctx.Value(cspNonceKey{}).(string); ok {
		return n
	}
	return ""
}

// Note: Google Fonts CSS responses are dynamic (vary by user-agent), so SRI
// cannot be applied. The CSP style-src restriction to fonts.googleapis.com
// is the appropriate control. All JavaScript vendor libraries are served
// locally from /static/vendor/, so no CDN script SRI is needed.

// SecurityHeaders is middleware that sets security-related HTTP headers on
// every response, protecting against clickjacking, MIME sniffing, and
// unauthorized script/resource loading.
//
// A per-request nonce is generated and stored in the request context for
// future use. Currently script-src uses 'unsafe-inline' because HTMX loads
// HTML fragments via AJAX whose inline scripts and event handlers cannot
// carry the page's nonce. Nonce-based CSP requires extracting all inline
// scripts to external files and replacing inline event handlers with
// addEventListener — a future effort.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := generateCSPNonce()

		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		ctx := context.WithValue(r.Context(), cspNonceKey{}, nonce)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
