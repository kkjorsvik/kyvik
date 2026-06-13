package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// RESTAPIEndpointList renders the REST API endpoints HTMX partial for an agent.
func (h *Handlers) RESTAPIEndpointList(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	endpoints := parseRESTAPIEndpoints(agent.RESTAPIEndpointsJSON)

	h.renderFragment(w, r, "restapi-endpoints", map[string]any{
		"Agent":     agent,
		"Endpoints": endpoints,
	})
}

// RESTAPIEndpointCreate adds a new REST API endpoint to an agent.
func (h *Handlers) RESTAPIEndpointCreate(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	ep := types.RESTAPIEndpoint{
		Name:        name,
		Description: r.FormValue("description"),
		Method:      strings.ToUpper(r.FormValue("method")),
		URL:         r.FormValue("url"),
		BodyTemplate: r.FormValue("body_template"),
		ResponseTemplate: r.FormValue("response_template"),
		Auth: types.RESTAPIAuth{
			Type:       r.FormValue("auth_type"),
			SecretRef:  r.FormValue("auth_secret_ref"),
			HeaderName: r.FormValue("auth_header_name"),
			ParamName:  r.FormValue("auth_param_name"),
		},
	}

	if v, err := strconv.Atoi(r.FormValue("cache_ttl_seconds")); err == nil {
		ep.CacheTTLSeconds = v
	}
	if v, err := strconv.Atoi(r.FormValue("rate_limit_rpm")); err == nil {
		ep.RateLimitRPM = v
	}
	if v, err := strconv.Atoi(r.FormValue("timeout_seconds")); err == nil {
		ep.TimeoutSeconds = v
	}

	ep.Headers = parseRESTAPIHeadersFromForm(r)
	ep.QueryParams = parseRESTAPIQueryParamsFromForm(r)

	if ep.Method == "" {
		ep.Method = "GET"
	}
	if ep.Auth.Type == "" {
		ep.Auth.Type = "none"
	}

	endpoints := parseRESTAPIEndpoints(agent.RESTAPIEndpointsJSON)
	endpoints = append(endpoints, ep)

	if err := saveRESTAPIEndpoints(h, r, agent, endpoints); err != nil {
		h.serverError(w, r, "saving REST API endpoints", err)
		return
	}

	h.renderFragment(w, r, "restapi-endpoints", map[string]any{
		"Agent":     agent,
		"Endpoints": endpoints,
	})
}

// RESTAPIEndpointEdit updates an existing REST API endpoint.
func (h *Handlers) RESTAPIEndpointEdit(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	epName := r.PathValue("name")
	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	endpoints := parseRESTAPIEndpoints(agent.RESTAPIEndpointsJSON)
	found := false
	for i := range endpoints {
		if endpoints[i].Name == epName {
			endpoints[i].Description = r.FormValue("description")
			endpoints[i].Method = strings.ToUpper(r.FormValue("method"))
			endpoints[i].URL = r.FormValue("url")
			endpoints[i].BodyTemplate = r.FormValue("body_template")
			endpoints[i].ResponseTemplate = r.FormValue("response_template")
			endpoints[i].Auth = types.RESTAPIAuth{
				Type:       r.FormValue("auth_type"),
				SecretRef:  r.FormValue("auth_secret_ref"),
				HeaderName: r.FormValue("auth_header_name"),
				ParamName:  r.FormValue("auth_param_name"),
			}
			if v, err := strconv.Atoi(r.FormValue("cache_ttl_seconds")); err == nil {
				endpoints[i].CacheTTLSeconds = v
			}
			if v, err := strconv.Atoi(r.FormValue("rate_limit_rpm")); err == nil {
				endpoints[i].RateLimitRPM = v
			}
			if v, err := strconv.Atoi(r.FormValue("timeout_seconds")); err == nil {
				endpoints[i].TimeoutSeconds = v
			}
			endpoints[i].Headers = parseRESTAPIHeadersFromForm(r)
			endpoints[i].QueryParams = parseRESTAPIQueryParamsFromForm(r)
			if endpoints[i].Auth.Type == "" {
				endpoints[i].Auth.Type = "none"
			}
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "endpoint not found", http.StatusNotFound)
		return
	}

	if err := saveRESTAPIEndpoints(h, r, agent, endpoints); err != nil {
		h.serverError(w, r, "saving REST API endpoints", err)
		return
	}

	h.renderFragment(w, r, "restapi-endpoints", map[string]any{
		"Agent":     agent,
		"Endpoints": endpoints,
	})
}

// RESTAPIEndpointDelete removes a REST API endpoint by name.
func (h *Handlers) RESTAPIEndpointDelete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	epName := r.PathValue("name")
	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	endpoints := parseRESTAPIEndpoints(agent.RESTAPIEndpointsJSON)
	filtered := make([]types.RESTAPIEndpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Name != epName {
			filtered = append(filtered, ep)
		}
	}

	if err := saveRESTAPIEndpoints(h, r, agent, filtered); err != nil {
		h.serverError(w, r, "saving REST API endpoints", err)
		return
	}

	h.renderFragment(w, r, "restapi-endpoints", map[string]any{
		"Agent":     agent,
		"Endpoints": filtered,
	})
}

// RESTAPIEndpointTest executes a test call to a configured endpoint.
func (h *Handlers) RESTAPIEndpointTest(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	epName := r.PathValue("name")
	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	endpoints := parseRESTAPIEndpoints(agent.RESTAPIEndpointsJSON)
	var ep *types.RESTAPIEndpoint
	for i := range endpoints {
		if endpoints[i].Name == epName {
			ep = &endpoints[i]
			break
		}
	}
	if ep == nil {
		http.Error(w, "endpoint not found", http.StatusNotFound)
		return
	}

	// Return the endpoint config as a JSON "test result" stub.
	// Full test execution would require wiring the tool executor, which is complex for the dashboard.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":   "stub",
		"endpoint": ep.Name,
		"method":   ep.Method,
		"url":      ep.URL,
		"message":  "Test endpoint " + ep.Name + " — use the agent's tool executor for full testing.",
	})
}

// --- helpers ---

func parseRESTAPIEndpoints(jsonStr string) []types.RESTAPIEndpoint {
	if jsonStr == "" {
		return nil
	}
	var endpoints []types.RESTAPIEndpoint
	_ = json.Unmarshal([]byte(jsonStr), &endpoints)
	return endpoints
}

func saveRESTAPIEndpoints(h *Handlers, r *http.Request, agent *types.AgentConfig, endpoints []types.RESTAPIEndpoint) error {
	data, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}
	agent.RESTAPIEndpointsJSON = string(data)
	agent.UpdatedAt = time.Now().UTC()
	return h.kyvik.UpdateAgent(r.Context(), *agent)
}

func parseRESTAPIHeadersFromForm(r *http.Request) map[string]string {
	_ = r.ParseForm()
	keys := r.Form["ep_header_key[]"]
	values := r.Form["ep_header_value[]"]
	headers := make(map[string]string)
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v := ""
		if i < len(values) {
			v = strings.TrimSpace(values[i])
		}
		headers[k] = v
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func parseRESTAPIQueryParamsFromForm(r *http.Request) map[string]string {
	_ = r.ParseForm()
	keys := r.Form["ep_param_key[]"]
	values := r.Form["ep_param_value[]"]
	params := make(map[string]string)
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v := ""
		if i < len(values) {
			v = strings.TrimSpace(values[i])
		}
		params[k] = v
	}
	if len(params) == 0 {
		return nil
	}
	return params
}
