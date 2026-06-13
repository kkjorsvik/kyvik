package restapi

import (
	"context"
	"net/http"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// EndpointConfigsFunc resolves the configured REST API endpoints for an agent.
type EndpointConfigsFunc func(agentID string) ([]types.RESTAPIEndpoint, error)

// SecretResolverFunc resolves a secret from the vault with cascading lookup
// (agent → team → global).
type SecretResolverFunc func(ctx context.Context, agentID, teamID, key string) (string, error)

// TierFunc resolves an agent's KTP tier.
type TierFunc func(agentID string) (string, error)

// Option configures a Tool.
type Option func(*Tool)

// AllowedHostsFunc resolves an agent's HTTP allowed hosts list.
type AllowedHostsFunc func(agentID string) ([]string, error)

// WithTierFunc sets the tier resolver callback.
func WithTierFunc(fn TierFunc) Option {
	return func(t *Tool) { t.tierFunc = fn }
}

// WithAllowedHostsFunc sets the allowed hosts resolver callback.
func WithAllowedHostsFunc(fn AllowedHostsFunc) Option {
	return func(t *Tool) { t.allowedHostsFunc = fn }
}

// WithTestTransport overrides the HTTP transport for testing.
func WithTestTransport(rt http.RoundTripper) Option {
	return func(t *Tool) { t.testTransport = rt }
}

// WithOAuth2SecretWriter sets the callback for writing OAuth2 tokens back to the vault.
func WithOAuth2SecretWriter(fn OAuth2SecretWriter) Option {
	return func(t *Tool) { t.oauth2SecretWriter = fn }
}
