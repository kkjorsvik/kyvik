package sandbox

// SecretsRequest is sent by the sandbox binary to request a secret value.
type SecretsRequest struct {
	Key string `json:"key"`
}

// SecretsResponse is the reply from the secrets server.
// Exactly one of Value or Error will be non-empty.
type SecretsResponse struct {
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}
