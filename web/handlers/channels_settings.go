package handlers

import (
	"net/http"
	"strings"
)

// ChannelSettingsList renders the channel settings page with a card per channel type.
func (h *Handlers) ChannelSettingsList(w http.ResponseWriter, r *http.Request) {
	if h.channelMgr == nil {
		http.Error(w, "Channel manager not configured", http.StatusServiceUnavailable)
		return
	}

	channels := h.channelMgr.ListChannels()

	data := map[string]any{
		"Nav":      "channels",
		"Title":    "Channels",
		"Channels": channels,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "channels-settings-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "channels-settings-list", data)
}

// ChannelSettingsEdit renders the edit form for a channel type.
func (h *Handlers) ChannelSettingsEdit(w http.ResponseWriter, r *http.Request) {
	if h.channelMgr == nil {
		http.Error(w, "Channel manager not configured", http.StatusServiceUnavailable)
		return
	}

	channelType := r.PathValue("type")
	ch := h.channelMgr.GetChannel(channelType)
	if ch == nil {
		http.Error(w, "Unknown channel type", http.StatusNotFound)
		return
	}

	tokens := h.channelMgr.GetTokens(r.Context(), channelType)

	data := map[string]any{
		"Nav":         "channels",
		"Title":       "Configure " + ch.DisplayName,
		"Channel":     ch,
		"ChannelType": channelType,
		"Tokens":      tokens,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "channels-settings-form", data)
		return
	}
	h.renderPageWithRequest(w, r, "channels-settings-form", data)
}

// ChannelSettingsSave saves tokens and enables/re-enables a channel.
func (h *Handlers) ChannelSettingsSave(w http.ResponseWriter, r *http.Request) {
	if h.channelMgr == nil {
		http.Error(w, "Channel manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	channelType := r.PathValue("type")
	tokens := make(map[string]string)

	switch channelType {
	case "slack":
		if v := strings.TrimSpace(r.FormValue("bot_token")); v != "" {
			tokens["bot_token"] = v
		}
		if v := strings.TrimSpace(r.FormValue("app_token")); v != "" {
			tokens["app_token"] = v
		}
		if r.FormValue("auto_provision") == "on" {
			tokens["auto_provision"] = "true"
		} else {
			tokens["auto_provision"] = "false"
		}
	case "discord":
		if v := strings.TrimSpace(r.FormValue("bot_token")); v != "" {
			tokens["bot_token"] = v
		}
		if v := strings.TrimSpace(r.FormValue("guild_id")); v != "" {
			tokens["guild_id"] = v
		}
		if r.FormValue("auto_provision") == "on" {
			tokens["auto_provision"] = "true"
		} else {
			tokens["auto_provision"] = "false"
		}
	default:
		http.Error(w, "unsupported channel type", http.StatusBadRequest)
		return
	}

	if err := h.channelMgr.EnableChannel(r.Context(), channelType, tokens); err != nil {
		h.serverError(w, r, "enabling channel", err)
		return
	}

	http.Redirect(w, r, "/channels/settings", http.StatusSeeOther)
}

// ChannelSettingsToggle enables or disables a channel.
func (h *Handlers) ChannelSettingsToggle(w http.ResponseWriter, r *http.Request) {
	if h.channelMgr == nil {
		http.Error(w, "Channel manager not configured", http.StatusServiceUnavailable)
		return
	}

	channelType := r.PathValue("type")
	ch := h.channelMgr.GetChannel(channelType)
	if ch == nil {
		http.Error(w, "Unknown channel type", http.StatusNotFound)
		return
	}

	if ch.Enabled {
		if err := h.channelMgr.DisableChannel(r.Context(), channelType); err != nil {
			h.serverError(w, r, "disabling channel", err)
			return
		}
	} else {
		if !ch.HasTokens {
			http.Redirect(w, r, "/channels/settings/"+channelType+"/edit", http.StatusSeeOther)
			return
		}
		if err := h.channelMgr.ReenableChannel(r.Context(), channelType); err != nil {
			h.serverError(w, r, "re-enabling channel", err)
			return
		}
	}

	h.ChannelSettingsList(w, r)
}

// ChannelSettingsTest tests connection with provided tokens.
func (h *Handlers) ChannelSettingsTest(w http.ResponseWriter, r *http.Request) {
	if h.channelMgr == nil {
		http.Error(w, "Channel manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	channelType := r.FormValue("channel_type")
	tokens := make(map[string]string)

	switch channelType {
	case "slack":
		tokens["bot_token"] = strings.TrimSpace(r.FormValue("bot_token"))
		tokens["app_token"] = strings.TrimSpace(r.FormValue("app_token"))
	case "discord":
		tokens["bot_token"] = strings.TrimSpace(r.FormValue("bot_token"))
		tokens["guild_id"] = strings.TrimSpace(r.FormValue("guild_id"))
	default:
		h.renderFragment(w, r, "channels-test-result", map[string]any{
			"Success": false,
			"Error":   "unsupported channel type",
		})
		return
	}

	err := h.channelMgr.TestConnection(r.Context(), channelType, tokens)
	if err != nil {
		h.renderFragment(w, r, "channels-test-result", map[string]any{
			"Success": false,
			"Error":   err.Error(),
		})
		return
	}

	h.renderFragment(w, r, "channels-test-result", map[string]any{
		"Success": true,
	})
}
