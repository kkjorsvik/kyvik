// Package integrations provides a template-based system for connecting agents
// to external services. Integration templates are YAML files that bundle REST API
// endpoints with optional skill prompts for a specific service.
package integrations

import (
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Template represents a parsed integration template YAML file.
type Template struct {
	Name        string                    `yaml:"name" json:"name"`
	DisplayName string                    `yaml:"display_name" json:"display_name"`
	Description string                    `yaml:"description" json:"description"`
	Version     string                    `yaml:"version" json:"version"`
	Category    types.IntegrationCategory `yaml:"category" json:"category"`
	Icon        string                    `yaml:"icon" json:"icon"`
	Auth        TemplateAuth              `yaml:"auth" json:"auth"`
	Variables   []TemplateVariable        `yaml:"variables,omitempty" json:"variables,omitempty"`
	Endpoints   []TemplateEndpoint        `yaml:"endpoints" json:"endpoints"`
	Prompts     *TemplatePrompts          `yaml:"prompts,omitempty" json:"prompts,omitempty"`

	// Runtime fields (not in YAML).
	Source   string `yaml:"-" json:"source"`    // "builtin" or "local"
	FilePath string `yaml:"-" json:"file_path"` // Absolute path to template file
}

// TemplateAuth defines the authentication requirements for an integration.
type TemplateAuth struct {
	Type         string `yaml:"type" json:"type"` // bearer, basic, api_key, custom_header, oauth2
	SecretRef    string `yaml:"secret_ref,omitempty" json:"secret_ref,omitempty"`
	HeaderName   string `yaml:"header_name,omitempty" json:"header_name,omitempty"`
	ParamName    string `yaml:"param_name,omitempty" json:"param_name,omitempty"`
	SetupURL     string `yaml:"setup_url,omitempty" json:"setup_url,omitempty"`
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`

	// OAuth2-specific fields.
	ClientIDRef     string `yaml:"client_id_ref,omitempty" json:"client_id_ref,omitempty"`
	ClientSecretRef string `yaml:"client_secret_ref,omitempty" json:"client_secret_ref,omitempty"`
	AuthURL         string `yaml:"auth_url,omitempty" json:"auth_url,omitempty"`
	TokenURL        string `yaml:"token_url,omitempty" json:"token_url,omitempty"`
	Scopes          string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

// TemplateVariable defines a user-configurable variable for the integration.
type TemplateVariable struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Required    bool   `yaml:"required" json:"required"`
	Default     string `yaml:"default,omitempty" json:"default,omitempty"`
	Example     string `yaml:"example,omitempty" json:"example,omitempty"`
}

// TemplateEndpoint defines a single REST API endpoint in a template.
type TemplateEndpoint struct {
	Name            string            `yaml:"name" json:"name"`
	Description     string            `yaml:"description" json:"description"`
	Method          string            `yaml:"method" json:"method"`
	URL             string            `yaml:"url" json:"url"`
	Headers         map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	QueryParams     map[string]string `yaml:"query_params,omitempty" json:"query_params,omitempty"`
	BodyTemplate    string            `yaml:"body_template,omitempty" json:"body_template,omitempty"`
	ResponseTemplate string           `yaml:"response_template,omitempty" json:"response_template,omitempty"`
	RateLimitRPM    int               `yaml:"rate_limit_rpm,omitempty" json:"rate_limit_rpm,omitempty"`
	CacheTTLSeconds int               `yaml:"cache_ttl_seconds,omitempty" json:"cache_ttl_seconds,omitempty"`
	TimeoutSeconds  int               `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	Parameters      []types.EndpointParam `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

// TemplatePrompts defines optional skill prompts bundled with an integration.
type TemplatePrompts struct {
	System string `yaml:"system,omitempty" json:"system,omitempty"`
}

// ToRESTAPIEndpoint converts a TemplateEndpoint to a types.RESTAPIEndpoint
// using the template's auth config and the given integration name.
func (te *TemplateEndpoint) ToRESTAPIEndpoint(auth TemplateAuth, integrationName string) types.RESTAPIEndpoint {
	ep := types.RESTAPIEndpoint{
		Name:              te.Name,
		Description:       te.Description,
		Method:            te.Method,
		URL:               te.URL,
		Headers:           te.Headers,
		QueryParams:       te.QueryParams,
		BodyTemplate:      te.BodyTemplate,
		ResponseTemplate:  te.ResponseTemplate,
		RateLimitRPM:      te.RateLimitRPM,
		CacheTTLSeconds:   te.CacheTTLSeconds,
		TimeoutSeconds:    te.TimeoutSeconds,
		Parameters:        te.Parameters,
		IntegrationSource: integrationName,
		Auth: types.RESTAPIAuth{
			Type:            auth.Type,
			SecretRef:       auth.SecretRef,
			HeaderName:      auth.HeaderName,
			ParamName:       auth.ParamName,
			ClientIDRef:     auth.ClientIDRef,
			ClientSecretRef: auth.ClientSecretRef,
			AuthURL:         auth.AuthURL,
			TokenURL:        auth.TokenURL,
			Scopes:          auth.Scopes,
		},
	}
	if ep.TimeoutSeconds == 0 {
		ep.TimeoutSeconds = 30
	}
	return ep
}
