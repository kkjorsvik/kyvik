package handlers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/internal/guide"
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/identity"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// TierInfo provides KTP tier details for template display in the wizard/edit pages.
type TierInfo struct {
	Tier         string   // KTP tier name (e.g., "admin")
	Capabilities []string // Human-readable: "filesystem: read", "shell: execute"
	Warning      string   // "", "caution", "danger"
	WarningText  string   // Warning message text
}

// buildTierInfoMap returns a map of template name → TierInfo for the 4 user-facing tiers.
func buildTierInfoMap(_ ...bool) map[string]TierInfo {
	templates := []string{"reader", "worker", "operator", "admin"}
	m := make(map[string]TierInfo, len(templates))
	for _, tmpl := range templates {
		tier := ktp.ResolveAgentTier(tmpl)
		if tier == "" {
			continue
		}
		info := TierInfo{Tier: tier}

		// Build human-readable capabilities
		switch tier {
		case ktp.TierReader:
			info.Capabilities = []string{"filesystem: read", "memory: read"}
		case ktp.TierWriter:
			info.Capabilities = []string{"filesystem: read, write", "memory: read, write", "network: read"}
		case ktp.TierOperator:
			info.Capabilities = []string{"filesystem: read, write", "memory: read, write", "network: read", "shell: execute", "process: execute"}
			info.Warning = "caution"
			info.WarningText = "Grants shell/process execution"
		case ktp.TierAdmin:
			info.Capabilities = []string{"filesystem: read, write", "memory: read, write", "network: read, write", "shell: execute", "process: execute"}
			info.Warning = "danger"
			info.WarningText = "Full system access — all capabilities including host filesystem"
		}

		m[tmpl] = info
	}
	return m
}

// slotNameRe validates slot names: lowercase, starts with letter, max 20 chars.
var slotNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

// parseSlotForm reads indexed slot fields from POST form (step 4 → step 5).
func parseSlotForm(r *http.Request) ([]router.ModelSlot, string, error) {
	countStr := r.FormValue("slot_count")
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		return nil, "", fmt.Errorf("invalid slot_count: %q", countStr)
	}

	var slots []router.ModelSlot
	defaultSlot := ""

	for i := 0; i < count; i++ {
		name := r.FormValue(fmt.Sprintf("slot_name_%d", i))
		provider := r.FormValue(fmt.Sprintf("slot_provider_%d", i))
		model := r.FormValue(fmt.Sprintf("slot_model_%d", i))
		isDefault := r.FormValue(fmt.Sprintf("slot_default_%d", i))

		// Skip rows that were removed (empty name)
		if name == "" && provider == "" && model == "" {
			continue
		}

		slots = append(slots, router.ModelSlot{
			Name:     name,
			Provider: provider,
			Model:    model,
		})

		if isDefault == "true" || isDefault == "on" {
			defaultSlot = name
		}
	}

	if defaultSlot == "" && len(slots) > 0 {
		defaultSlot = slots[0].Name
	}

	return slots, defaultSlot, nil
}

// parseSlotJSON reads model_slots_json hidden field (steps 5-8, create, edit).
func parseSlotJSON(jsonStr string) ([]router.ModelSlot, error) {
	if jsonStr == "" {
		return nil, nil
	}
	var slots []router.ModelSlot
	if err := json.Unmarshal([]byte(jsonStr), &slots); err != nil {
		return nil, fmt.Errorf("invalid model slots JSON: %w", err)
	}
	return slots, nil
}

// slotFormToJSON converts slots + default + routing options to JSON strings for hidden fields.
func slotFormToJSON(slots []router.ModelSlot, defaultSlot string, routingOpts map[string]string) (slotsJSON, routingJSON string, err error) {
	slotsBytes, err := json.Marshal(slots)
	if err != nil {
		return "", "", fmt.Errorf("marshal slots: %w", err)
	}

	rc := router.RoutingConfig{
		DefaultSlot:    defaultSlot,
		ClassifierSlot: routingOpts["classifier_slot"],
		FallbackSlot:   routingOpts["fallback_slot"],
		AutoRoute:      routingOpts["auto_route"] == "true" || routingOpts["auto_route"] == "on",
		TriggerPrefix:  routingOpts["trigger_prefix"] == "true" || routingOpts["trigger_prefix"] == "on",
	}
	rcBytes, err := json.Marshal(rc)
	if err != nil {
		return "", "", fmt.Errorf("marshal routing config: %w", err)
	}

	return string(slotsBytes), string(rcBytes), nil
}

// validateSlots checks: ≥1 slot, exactly 1 default, no duplicate names, valid names, valid providers.
func validateSlots(slots []router.ModelSlot, defaultSlot string, providerNames []string) []string {
	var errs []string

	if len(slots) == 0 {
		errs = append(errs, "At least one model slot is required.")
		return errs
	}

	providerSet := make(map[string]bool, len(providerNames))
	for _, p := range providerNames {
		providerSet[p] = true
	}

	namesSeen := make(map[string]bool, len(slots))
	defaultFound := false

	for _, slot := range slots {
		if !slotNameRe.MatchString(slot.Name) {
			errs = append(errs, fmt.Sprintf("Slot name %q is invalid (lowercase letters, digits, hyphens; max 20 chars; must start with letter).", slot.Name))
		}
		if namesSeen[slot.Name] {
			errs = append(errs, fmt.Sprintf("Duplicate slot name %q.", slot.Name))
		}
		namesSeen[slot.Name] = true

		if !providerSet[slot.Provider] {
			errs = append(errs, fmt.Sprintf("Slot %q: unknown provider %q.", slot.Name, slot.Provider))
		}
		if slot.Model == "" {
			errs = append(errs, fmt.Sprintf("Slot %q: model is required.", slot.Name))
		}

		if slot.Name == defaultSlot {
			defaultFound = true
		}
	}

	if !defaultFound {
		errs = append(errs, fmt.Sprintf("Default slot %q not found in slot list.", defaultSlot))
	}

	return errs
}

// AgentList renders the agent list page.
func (h *Handlers) AgentList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "listing agents", err)
		return
	}
	agents, err = h.filterAgentsForUser(ctx, agents)
	if err != nil {
		h.serverError(w, r, "applying agent scope", err)
		return
	}

	// Build node name lookup when clustering is active.
	clusterEnabled := h.clusterMgr != nil
	nodeNames := map[string]string{}
	if clusterEnabled {
		if nodes, err := h.clusterMgr.ListNodes(); err == nil {
			for _, n := range nodes {
				nodeNames[n.NodeID] = n.NodeName
			}
		}
	}

	cards := make([]AgentCard, 0, len(agents))
	for _, a := range agents {
		status, _ := h.kyvik.GetAgentStatus(ctx, a.ID)
		card := AgentCard{AgentConfig: a, Status: status}
		if clusterEnabled {
			if nodeID, err := h.clusterMgr.GetAssignment(a.ID); err == nil && nodeID != "" {
				card.AssignedNodeID = nodeID
				card.AssignedNodeName = nodeNames[nodeID]
			}
		}
		cards = append(cards, card)
	}

	// Sort guide agent first.
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].IsGuide != cards[j].IsGuide {
			return cards[i].IsGuide
		}
		return false
	})

	data := map[string]any{
		"Nav":            "agents",
		"Title":          "Agents",
		"Agents":         cards,
		"ClusterEnabled": clusterEnabled,
	}

	if isHTMX(r) {
		h.injectTemplateUser(ctx, data)
		h.renderFragment(w, r, "agents-list", data)
		return
	}

	h.renderPageWithRequest(w, r, "agents-list", data)
}

// buildAgentDetailData builds the full template data map for an agent's detail page.
// This is shared between AgentDetail (full page), AgentEditSectionGet (modal),
// and AgentEditSectionPost (card re-render after save).
func (h *Handlers) buildAgentDetailData(ctx context.Context, agent *types.AgentConfig) map[string]any {
	id := agent.ID

	status, _ := h.kyvik.GetAgentStatus(ctx, id)

	// Budget status (nil-safe)
	var budget interface{}
	if sp := h.kyvik.Spending(); sp != nil {
		budget, _ = sp.CheckBudget(ctx, id)
	}

	// Memory count (nil-safe)
	var memoryCount int
	if ms := h.kyvik.Storage.Memory; ms != nil {
		memoryCount, _ = ms.Count(ctx, id)
	}

	// History count (nil-safe)
	var historyCount int
	if hs := h.kyvik.Storage.History; hs != nil {
		historyCount, _ = hs.Count(ctx, id, "", "")
	}

	// Recent audit entries (nil-safe)
	var auditEntries []types.AuditEntry
	if al := h.kyvik.Audit(); al != nil {
		auditEntries, _ = al.Query(ctx, audit.Filter{AgentID: id, Limit: 10})
	}

	// Per-agent API key status (nil-safe)
	var hasDedicatedKey bool
	if h.secrets != nil {
		hasDedicatedKey, _ = h.secrets.Exists(ctx, "agent:"+id, "openrouter:api_key")
	}

	// Slot usage breakdown (nil-safe, only for multi-slot agents)
	var slotBreakdown []spending.SlotUsageSummary
	if sp := h.kyvik.Spending(); sp != nil && agent.ModelSlotsJSON != "" {
		slotBreakdown, _ = sp.GetSlotBreakdown(ctx, id, "day")
	}

	// Active workers (nil-safe)
	var activeWorkerCount int
	if wm := h.kyvik.WorkerManager(); wm != nil {
		activeWorkerCount = wm.ActiveCount(id)
	}

	// Routing stats (nil-safe, only for multi-slot agents)
	var routingStats *router.RoutingStats
	if agent.ModelSlotsJSON != "" {
		if rt := h.kyvik.Communication.Router; rt != nil {
			snapshot := rt.Stats(id)
			routingStats = &snapshot
		}
	}

	// Parse slots for detail display
	var detailSlots []router.ModelSlot
	var detailRoutingConfig router.RoutingConfig
	if agent.ModelSlotsJSON != "" {
		json.Unmarshal([]byte(agent.ModelSlotsJSON), &detailSlots)
		if agent.RoutingConfigJSON != "" {
			json.Unmarshal([]byte(agent.RoutingConfigJSON), &detailRoutingConfig)
		}
	}

	// Tool info
	var agentTools []ktp.ToolDeclaration
	agentTier := ""
	if reg := h.kyvik.KTPRegistry(); reg != nil {
		agentTier = ktp.ResolveAgentTier(agent.Template)
		agentTools = reg.ListForAgent(id, agentTier, agent.ToolGrants)
	}

	// Circuit breaker status (nil-safe)
	var breakerStatus *circuitbreaker.BreakerStatus
	if bm := h.kyvik.Lifecycle.Breaker; bm != nil {
		bs := bm.Status(id)
		breakerStatus = &bs
	}
	var breakerConfig types.CircuitBreakerConfig
	var systemBreakerDefaults types.CircuitBreakerConfig
	if bm := h.kyvik.Lifecycle.Breaker; bm != nil {
		systemBreakerDefaults = bm.SystemDefaults()
		breakerConfig = circuitbreaker.ResolveConfig(*agent, systemBreakerDefaults)
	} else {
		systemBreakerDefaults = types.DefaultCircuitBreakerConfig()
		breakerConfig = circuitbreaker.ResolveConfig(*agent, types.CircuitBreakerConfig{})
	}

	// Tool execution audit entries (separate from general audit)
	var toolAuditEntries []types.AuditEntry
	if al := h.kyvik.Audit(); al != nil {
		toolAuditEntries, _ = al.Query(ctx, audit.Filter{
			AgentID:   id,
			EventType: types.EventToolCall,
			Limit:     10,
		})
	}

	// Schedules (nil-safe)
	var schedules []types.Schedule
	var heartbeatSchedule *types.Schedule
	hasScheduler := h.kyvik.Lifecycle.Scheduler != nil
	if sched := h.kyvik.Lifecycle.Scheduler; sched != nil {
		schedules, _ = sched.ListByType(ctx, id, types.ScheduleTypeTask)
		heartbeatSchedule, _ = sched.GetHeartbeatSchedule(ctx, id)
	}

	// Heartbeat config
	var heartbeatConfig types.HeartbeatConfig
	if agent.HeartbeatJSON != "" {
		json.Unmarshal([]byte(agent.HeartbeatJSON), &heartbeatConfig)
	}

	// Compression config
	var compressionConfig types.CompressionConfig
	if agent.CompressionJSON != "" {
		json.Unmarshal([]byte(agent.CompressionJSON), &compressionConfig)
	}

	// Feedback hooks config
	var feedbackHooksConfig types.FeedbackHooksConfig
	if agent.FeedbackHooksJSON != "" {
		json.Unmarshal([]byte(agent.FeedbackHooksJSON), &feedbackHooksConfig)
	}

	// Team membership (nil-safe)
	var team *types.Team
	isTeamLeader := false
	if tm := h.kyvik.TeamManager(); tm != nil {
		if t, err := tm.GetTeamForAgent(ctx, id); err == nil && t != nil {
			team = t
			isTeamLeader = t.LeaderID == id
		}
	}

	// Security config
	var securityConfig types.SecurityConfig
	if agent.SecurityJSON != "" {
		json.Unmarshal([]byte(agent.SecurityJSON), &securityConfig)
	} else {
		securityConfig = types.DefaultSecurityConfig()
	}

	// REST API endpoints
	var restEndpoints []types.RESTAPIEndpoint
	if agent.RESTAPIEndpointsJSON != "" {
		json.Unmarshal([]byte(agent.RESTAPIEndpointsJSON), &restEndpoints)
	}

	// Available tools for edit modals
	var availableTools []ktp.ToolDeclaration
	if reg := h.kyvik.KTPRegistry(); reg != nil {
		if agentTier == "" {
			agentTier = ktp.ResolveAgentTier(agent.Template)
		}
		availableTools = reg.ListForTier(agentTier)
	}
	toolGrantSet := make(map[string]bool, len(agent.ToolGrants))
	for _, g := range agent.ToolGrants {
		toolGrantSet[g] = true
	}

	// Build default tool set for edit modal badges.
	defaultToolSet := make(map[string]bool)
	if reg := h.kyvik.KTPRegistry(); reg != nil {
		for _, name := range reg.DefaultToolsForTier(agentTier) {
			defaultToolSet[name] = true
		}
	}

	// Slack / Discord available
	slackAvailable := false
	discordAvailable := false
	for _, name := range h.kyvik.ListChannelNames() {
		if name == "slack" {
			slackAvailable = true
		}
		if name == "discord" {
			discordAvailable = true
		}
	}

	// All agents for can_message reference
	allAgents, _ := h.kyvik.ListAgents(ctx)

	// Providers for model edit
	providers := h.kyvik.ListProviders()

	data := map[string]any{
		"Nav":               "agents",
		"Title":             agent.Name,
		"Agent":             agent,
		"Status":            status,
		"Budget":            budget,
		"MemoryCount":       memoryCount,
		"HistoryCount":      historyCount,
		"AuditEntries":      auditEntries,
		"HasDedicatedKey":   hasDedicatedKey,
		"SlotBreakdown":     slotBreakdown,
		"HasMultipleSlots":  agent.ModelSlotsJSON != "",
		"Slots":             detailSlots,
		"RoutingConfig":     detailRoutingConfig,
		"ActiveWorkerCount": activeWorkerCount,
		"RoutingStats":      routingStats,
		"AgentTier":         agentTier,
		"AgentTools":        agentTools,
		"AvailableTools":    availableTools,
		"ToolGrantSet":      toolGrantSet,
		"DefaultToolSet":    defaultToolSet,
		"ToolAuditEntries":  toolAuditEntries,
		"BreakerStatus":          breakerStatus,
		"BreakerConfig":          breakerConfig,
		"SystemBreakerDefaults":  systemBreakerDefaults,
		"EmergencyStop":     h.kyvik.EmergencyStopActive(),
		"Schedules":         schedules,
		"HeartbeatConfig":   heartbeatConfig,
		"HeartbeatSchedule": heartbeatSchedule,
		"HeartbeatPresets":  identity.GetHeartbeatPresets(),
		"CompressionConfig":    compressionConfig,
		"FeedbackHooksConfig":  feedbackHooksConfig,
		"HasScheduler":      hasScheduler,
		"Team":              team,
		"IsTeamLeader":      isTeamLeader,
		"HasQueue":          h.kyvik.Storage.Queue != nil,
		"SecurityConfig":    securityConfig,
		"RESTEndpoints":     restEndpoints,
		"SlackAvailable":    slackAvailable,
		"DiscordAvailable":  discordAvailable,
		"AllAgents":         allAgents,
		"Providers":         providers,
	}

	// Workflows (nil-safe)
	var workflows []types.Workflow
	if s, ok := h.kyvik.Store().(store.Store); ok && s != nil {
		workflows, _ = s.ListWorkflows(ctx, id)
	}
	data["Workflows"] = workflows

	// Granted skills (nil-safe)
	var grantedSkills []skills.GrantedSkill
	if sm := h.kyvik.SkillManager(); sm != nil {
		grantedSkills, _ = sm.ListGrants(ctx, id)
	}
	data["GrantedSkills"] = grantedSkills

	// Cluster placement (nil-safe)
	if h.clusterMgr != nil {
		data["ClusterEnabled"] = true
		if nodeID, err := h.clusterMgr.GetAssignment(id); err == nil && nodeID != "" {
			data["AgentNodeID"] = nodeID
			if nodes, err := h.clusterMgr.ListNodes(); err == nil {
				for _, n := range nodes {
					if n.NodeID == nodeID {
						data["AgentNodeName"] = n.NodeName
						break
					}
				}
			}
		}
	}

	// Obsidian vaults (nil-safe)
	if h.obsidianMgr != nil {
		data["ObsidianEnabled"] = true
		allVaults, _ := h.obsidianMgr.ListVaults(ctx)
		data["AvailableVaults"] = allVaults
	}

	// Workspace size (with 2s timeout to avoid blocking page render)
	wsRoot := h.kyvik.WorkspaceRoot()
	wsPath := filepath.Join(wsRoot, id)
	type sizeResult struct {
		size int64
		err  error
	}
	ch := make(chan sizeResult, 1)
	go func() {
		s, e := dirSize(wsPath)
		ch <- sizeResult{s, e}
	}()
	select {
	case res := <-ch:
		if res.err == nil && res.size > 0 {
			data["WorkspaceSize"] = humanize.Bytes(uint64(res.size))
		} else {
			data["WorkspaceSize"] = ""
		}
	case <-time.After(2 * time.Second):
		data["WorkspaceSize"] = ""
	}

	h.injectTemplateUser(ctx, data)

	return data
}

// WorkspaceDownload serves the agent's workspace directory as a tar.gz download.
func (h *Handlers) WorkspaceDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "missing agent ID", http.StatusBadRequest)
		return
	}

	wsRoot := h.kyvik.WorkspaceRoot()
	wsPath := filepath.Join(wsRoot, agentID)

	// Check workspace exists
	info, err := os.Stat(wsPath)
	if err != nil || !info.IsDir() {
		http.Error(w, "No workspace files found", http.StatusNotFound)
		return
	}

	// Check size (500 MB limit for web download)
	size, err := dirSize(wsPath)
	if err != nil {
		http.Error(w, "Failed to calculate workspace size", http.StatusInternalServerError)
		return
	}
	if size > 500*1024*1024 {
		http.Error(w, fmt.Sprintf("Workspace too large for web download (%s). Use CLI to export.", humanize.Bytes(uint64(size))), http.StatusRequestEntityTooLarge)
		return
	}

	// Try to get agent name for filename
	filename := agentID + "-workspace.tar.gz"
	if agent, err := h.kyvik.GetAgent(r.Context(), agentID); err == nil && agent != nil {
		filename = agent.Name + "-workspace.tar.gz"
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", "application/gzip")

	// Create tar.gz and stream to response
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	filepath.WalkDir(wsPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(wsPath, path)
		if relPath == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !d.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			io.Copy(tw, f)
		}
		return nil
	})
}

// dirSize calculates total size of a directory recursively.
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// AgentDetail renders the agent detail/overview page.
func (h *Handlers) AgentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	data := h.buildAgentDetailData(ctx, config)

	if isHTMX(r) {
		h.renderFragment(w, r, "agent-detail", data)
		return
	}
	h.renderPageWithRequest(w, r, "agent-detail", data)
}

// wizardData extracts all wizard form values from the request into a template data map.
func wizardData(r *http.Request) map[string]any {
	webuiEnabled := r.FormValue("webui_enabled")
	if webuiEnabled == "" {
		webuiEnabled = "true"
	}

	slackMode := r.FormValue("slack_mode")
	if slackMode == "" {
		slackMode = "none"
	}

	discordMode := r.FormValue("discord_mode")
	if discordMode == "" {
		discordMode = "none"
	}

	showAdvanced := r.FormValue("show_advanced") == "true"
	if r.FormValue("show_advanced") == "" {
		showAdvanced = false
	}

	return map[string]any{
		"Nav":                                 "agents",
		"Title":                               "Create Agent",
		"WizardMode":                          r.FormValue("wizard_mode"),
		"AgentID":                             r.FormValue("agent_id"),
		"ShowAdvanced":                        showAdvanced,
		"FromTemplateID":                      r.FormValue("from_template_id"),
		"LockedJSON":                          r.FormValue("locked_json"),
		"ConstrainedJSON":                     r.FormValue("constrained_json"),
		"Name":                                r.FormValue("name"),
		"Description":                         r.FormValue("description"),
		"SystemPrompt":                        r.FormValue("system_prompt"),
		"SoulContent":                         r.FormValue("soul_content"),
		"IdentityContent":                     r.FormValue("identity_content"),
		"SoulTab":                             r.FormValue("soul_tab"),
		"IdentityTab":                         r.FormValue("identity_tab"),
		"Provider":                            r.FormValue("provider"),
		"Model":                               r.FormValue("model"),
		"ModelSlotsJSON":                      r.FormValue("model_slots_json"),
		"RoutingConfigJSON":                   r.FormValue("routing_config_json"),
		"SelectedTemplate":                    r.FormValue("template"),
		"SelectedSkillsJSON":                  r.FormValue("skills_json"),
		"MaxTokensPerDay":                     r.FormValue("max_tokens_per_day"),
		"MaxTokensPerMonth":                   r.FormValue("max_tokens_per_month"),
		"MaxSpendPerDay":                      r.FormValue("max_spend_per_day"),
		"MaxSpendPerMonth":                    r.FormValue("max_spend_per_month"),
		"HistoryLimit":                        r.FormValue("history_limit"),
		"MemoryLimit":                         r.FormValue("memory_limit"),
		"AutoExtractMemories":                 r.FormValue("auto_extract_memories"),
		"MaxMemories":                         r.FormValue("max_memories"),
		"MemoryExtractionInterval":            r.FormValue("memory_extraction_interval"),
		"MemoryMaxExtractionsPerRun":          r.FormValue("memory_max_extractions_per_run"),
		"MemoryDuplicateThreshold":            r.FormValue("memory_duplicate_threshold"),
		"MemorySimilarThreshold":              r.FormValue("memory_similar_threshold"),
		"TimestampMessages":                   r.FormValue("timestamp_messages"),
		"MaxTotalTokens":                      r.FormValue("max_total_tokens"),
		"SoulIdentityPct":                     r.FormValue("soul_identity_pct"),
		"SkillsPct":                           r.FormValue("skills_pct"),
		"MemoriesPct":                         r.FormValue("memories_pct"),
		"HistoryPct":                          r.FormValue("history_pct"),
		"SlackMode":                           slackMode,
		"SlackChannel":                        r.FormValue("slack_channel"),
		"DiscordMode":                         discordMode,
		"DiscordChannelID":                    r.FormValue("discord_channel_id"),
		"WebUIEnabled":                        webuiEnabled,
		"CanMessage":                          r.FormValue("can_message"),
		"WorkersEnabled":                      r.FormValue("workers_enabled"),
		"WorkersMaxConcurrent":                r.FormValue("workers_max_concurrent"),
		"WorkersTTLSeconds":                   r.FormValue("workers_ttl_seconds"),
		"WorkersModelSlot":                    r.FormValue("workers_model_slot"),
		"ToolGrantsJSON":                      r.FormValue("tool_grants_json"),
		"CapabilityGrantsJSON":                r.FormValue("capability_grants_json"),
		"TierAcknowledged":                    r.FormValue("tier_acknowledged"),
		"TierConfirmName":                     r.FormValue("tier_confirm_name"),
		"CircuitBreakerEnabled":               r.FormValue("circuit_breaker_enabled"),
		"CircuitBreakerErrorThreshold":        r.FormValue("circuit_breaker_error_threshold"),
		"CircuitBreakerErrorWindowMinutes":    r.FormValue("circuit_breaker_error_window_minutes"),
		"CircuitBreakerSpendingVelocityPct":   r.FormValue("circuit_breaker_spending_velocity_pct"),
		"CircuitBreakerSpendingWindowMinutes": r.FormValue("circuit_breaker_spending_window_minutes"),
		"CircuitBreakerActionRatePerMinute":   r.FormValue("circuit_breaker_action_rate_per_minute"),
		"CircuitBreakerDestructiveLimit":      r.FormValue("circuit_breaker_destructive_limit"),
		"CircuitBreakerLoopIdenticalCount":    r.FormValue("circuit_breaker_loop_identical_count"),
		"HostPathsRead":                       r.FormValue("host_paths_read"),
		"HostPathsWrite":                      r.FormValue("host_paths_write"),
		"HostPathsDeny":                       r.FormValue("host_paths_deny"),
		"HTTPAllowedHosts":                    r.FormValue("http_allowed_hosts"),
		"ShellAllowedCommands":                r.FormValue("shell_allowed_commands"),
		"HeartbeatEnabled":                    r.FormValue("heartbeat_enabled"),
		"HeartbeatInterval":                   r.FormValue("heartbeat_interval"),
		"HeartbeatPrompt":                     r.FormValue("heartbeat_prompt"),
		"HeartbeatQuietHours":                 r.FormValue("heartbeat_quiet_hours"),
		"HeartbeatPresets":                    identity.GetHeartbeatPresets(),
		"IntegrationsJSON":                    r.FormValue("integrations_json"),
		"ScheduleDraftsJSON":                  r.FormValue("schedule_drafts_json"),
		"Error":                               "",
	}
}

func wizardAdvancedAllowed(r *http.Request) bool {
	if u, ok := currentDashboardUser(r.Context()); ok {
		role := roleForDashboardUser(u.IsAdmin, u.Role)
		return role == auth.RoleManager || role == auth.RoleAdmin
	}
	return false
}

// AgentWizardStep1 renders the first wizard step (full page GET).
func (h *Handlers) AgentWizardStep1(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Nav":             "agents",
		"Title":           "Create Agent",
		"WizardMode":      "create",
		"ShowAdvanced":    false,
		"AdvancedAllowed": wizardAdvancedAllowed(r),
		"Name":            "",
		"Description":     "",
		"Error":           "",
	}

	// Inject available templates for users who lack unrestricted create.
	if h.templateSvc != nil {
		ctx := r.Context()
		if u, ok := currentDashboardUser(ctx); ok {
			canUnrestricted := auth.Can(u.Role, auth.PermAgentCreateUnrestricted)
			if u.IsAdmin {
				canUnrestricted = true
			}
			if !canUnrestricted {
				// Manager: must use a template.
				if h.userSvc != nil {
					roles, _ := h.userSvc.UserGroupRoles(ctx, u.ID)
					groupIDs := make([]string, len(roles))
					for i, r := range roles {
						groupIDs[i] = r.GroupID
					}
					templates, _ := h.templateSvc.ListForGroups(ctx, groupIDs)
					data["AvailableTemplates"] = templates
					data["RequireTemplate"] = true
				}
			} else {
				// Admin: templates are optional convenience.
				templates, _ := h.templateSvc.List(ctx)
				if len(templates) > 0 {
					data["AvailableTemplates"] = templates
				}
			}
		}
	}

	templates, err := h.kyvik.ListTemplates(r.Context())
	if err == nil {
		data["Templates"] = templates
		data["TierInfo"] = buildTierInfoMap(h.kyvik.AllowUnrestricted())
	}

	h.renderPageWithRequest(w, r, "agents-new", data)
}

// AgentTemplateSelectPage renders the template selection page for the "From Template" creation flow.
func (h *Handlers) AgentTemplateSelectPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Nav":   "agents",
		"Title": "New Agent from Template",
	}

	if h.templateSvc != nil {
		ctx := r.Context()
		u, ok := currentDashboardUser(ctx)
		if !ok {
			h.handleAuthRedirect(w, r)
			return
		}

		canUnrestricted := u.IsAdmin || auth.Can(u.Role, auth.PermAgentCreateUnrestricted)

		if !canUnrestricted && h.userSvc != nil {
			roles, _ := h.userSvc.UserGroupRoles(ctx, u.ID)
			groupIDs := make([]string, len(roles))
			for i, r := range roles {
				groupIDs[i] = r.GroupID
			}
			templates, _ := h.templateSvc.ListForGroups(ctx, groupIDs)
			data["AvailableTemplates"] = templates
		} else {
			templates, _ := h.templateSvc.List(ctx)
			data["AvailableTemplates"] = templates
		}
	}

	h.renderPageWithRequest(w, r, "agents-new-from-template", data)
}

// AgentTemplateConfigurePage renders the configure form for a chosen agent template (GET).
func (h *Handlers) AgentTemplateConfigurePage(w http.ResponseWriter, r *http.Request) {
	templateID := r.PathValue("template_id")
	ctx := r.Context()

	if h.templateSvc == nil {
		http.Error(w, "templates not available", http.StatusInternalServerError)
		return
	}

	cfg, err := h.templateSvc.ConfigFromTemplate(ctx, templateID)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	tmpl, err := h.templateSvc.Get(ctx, templateID)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	providers := h.kyvik.ListProviders()
	permTemplates, _ := h.kyvik.ListTemplates(ctx)

	toolGrantsJSON, _ := json.Marshal(cfg.ToolGrants)
	capGrantsJSON, _ := json.Marshal(cfg.CapabilityGrants)

	historyLimit := cfg.HistoryLimit
	if historyLimit <= 0 {
		historyLimit = 50
	}
	memoryLimit := cfg.MemoryLimit
	if memoryLimit <= 0 {
		memoryLimit = 10
	}

	data := map[string]any{
		"Nav":               "agents",
		"Title":             "Configure Agent from Template",
		"TemplateID":        templateID,
		"TemplateName":      tmpl.Name,
		"Name":              "",
		"Description":       tmpl.Description,
		"SelectedProvider":  cfg.ModelConfig.Provider,
		"Model":             cfg.ModelConfig.Model,
		"SelectedTemplate":  cfg.Template,
		"SystemPrompt":      cfg.SystemPrompt,
		"SoulContent":       cfg.SoulContent,
		"IdentityContent":   cfg.IdentityContent,
		"HistoryLimit":      historyLimit,
		"MemoryLimit":       memoryLimit,
		"WebUIEnabled":      cfg.WebUIEnabled,
		"SlackMode":         cfg.SlackMode,
		"SlackChannel":      cfg.SlackChannel,
		"DiscordMode":       cfg.DiscordMode,
		"DiscordChannelID":  cfg.DiscordChannelID,
		"Providers":         providers,
		"Templates":         permTemplates,
		"ModelSlotsJSON":    cfg.ModelSlotsJSON,
		"RoutingConfigJSON": cfg.RoutingConfigJSON,
		"ToolGrantsJSON":    string(toolGrantsJSON),
		"CapabilityGrantsJSON": string(capGrantsJSON),
		"SecurityJSON":      cfg.SecurityJSON,
		"CircuitBreakerJSON": cfg.CircuitBreakerJSON,
		"HeartbeatJSON":     cfg.HeartbeatJSON,
		"CompressionJSON":   cfg.CompressionJSON,
		"FeedbackHooksJSON": cfg.FeedbackHooksJSON,
	}

	h.renderPageWithRequest(w, r, "agents-configure-from-template", data)
}

// AgentTemplateCreate handles agent creation from a template form (POST).
func (h *Handlers) AgentTemplateCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.FormValue("name") == "" || r.FormValue("provider") == "" || r.FormValue("model") == "" || r.FormValue("template") == "" {
		h.renderTemplateFormWithError(w, r, "Missing required fields: name, provider, model, and permission tier are all required.")
		return
	}

	config := buildConfigFromRequest(r)
	config.ID = uuid.New().String()
	config.CreatedAt = timeutil.NowUTC()
	config.UpdatedAt = timeutil.NowUTC()

	if err := h.finalizeAgentCreate(ctx, r, &config); err != nil {
		h.renderTemplateFormWithError(w, r, err.Error())
		return
	}

	w.Header().Set("HX-Redirect", "/agents")
	w.WriteHeader(http.StatusOK)
}

// renderTemplateFormWithError re-renders the template configure form with an error message,
// preserving user input from the submitted form.
func (h *Handlers) renderTemplateFormWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	ctx := r.Context()
	templateID := r.FormValue("from_template_id")
	if templateID == "" {
		templateID = r.PathValue("template_id")
	}

	config := buildConfigFromRequest(r)
	providers := h.kyvik.ListProviders()
	permTemplates, _ := h.kyvik.ListTemplates(ctx)

	templateName := ""
	if h.templateSvc != nil && templateID != "" {
		if tmpl, err := h.templateSvc.Get(ctx, templateID); err == nil {
			templateName = tmpl.Name
		}
	}

	toolGrantsJSON := r.FormValue("tool_grants_json")
	capGrantsJSON := r.FormValue("capability_grants_json")

	data := map[string]any{
		"Nav":               "agents",
		"Title":             "Configure Agent from Template",
		"Error":             errMsg,
		"TemplateID":        templateID,
		"TemplateName":      templateName,
		"Name":              config.Name,
		"Description":       config.Description,
		"SelectedProvider":  config.ModelConfig.Provider,
		"Model":             config.ModelConfig.Model,
		"SelectedTemplate":  config.Template,
		"SystemPrompt":      config.SystemPrompt,
		"SoulContent":       config.SoulContent,
		"IdentityContent":   config.IdentityContent,
		"HistoryLimit":      config.HistoryLimit,
		"MemoryLimit":       config.MemoryLimit,
		"WebUIEnabled":      config.WebUIEnabled,
		"SlackMode":         config.SlackMode,
		"SlackChannel":      config.SlackChannel,
		"DiscordMode":       config.DiscordMode,
		"DiscordChannelID":  config.DiscordChannelID,
		"Providers":         providers,
		"Templates":         permTemplates,
		"ModelSlotsJSON":    config.ModelSlotsJSON,
		"RoutingConfigJSON": config.RoutingConfigJSON,
		"ToolGrantsJSON":    toolGrantsJSON,
		"CapabilityGrantsJSON": capGrantsJSON,
		"SecurityJSON":      config.SecurityJSON,
		"CircuitBreakerJSON": config.CircuitBreakerJSON,
		"HeartbeatJSON":     config.HeartbeatJSON,
		"CompressionJSON":   config.CompressionJSON,
		"FeedbackHooksJSON": config.FeedbackHooksJSON,
	}

	h.renderPageWithRequest(w, r, "agents-configure-from-template", data)
}

// AgentWizardStep1Post re-renders step 1 from a POST (back button).
func (h *Handlers) AgentWizardStep1Post(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)
	templates, err := h.kyvik.ListTemplates(r.Context())
	if err == nil {
		data["Templates"] = templates
		data["TierInfo"] = buildTierInfoMap(h.kyvik.AllowUnrestricted())
	}
	h.renderFragment(w, r, "wizard-step1", data)
}

// AgentWizardStep2 validates step 1 (basics) and renders step 2 (soul).
func (h *Handlers) AgentWizardStep2(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Validate step 1
	if r.FormValue("name") == "" || r.FormValue("template") == "" {
		if r.FormValue("name") == "" {
			data["Error"] = "Name is required."
		} else {
			data["Error"] = "Permission tier is required."
		}
		templates, err := h.kyvik.ListTemplates(r.Context())
		if err == nil {
			data["Templates"] = templates
			data["TierInfo"] = buildTierInfoMap(h.kyvik.AllowUnrestricted())
		}
		h.renderFragment(w, r, "wizard-step1", data)
		return
	}

	data["SoulPresets"] = identity.GetSoulPresets()
	data["RoleTemplates"] = identity.GetRoleTemplates()

	// Auto-select "custom" tab when template content exists.
	if sc, _ := data["SoulContent"].(string); sc != "" {
		if st, _ := data["SoulTab"].(string); st == "" || st == "presets" {
			data["SoulTab"] = "custom"
		}
	}
	if ic, _ := data["IdentityContent"].(string); ic != "" {
		if it, _ := data["IdentityTab"].(string); it == "" || it == "presets" {
			data["IdentityTab"] = "custom"
		}
	}

	h.renderFragment(w, r, "wizard-step2", data)
}

// AgentWizardStep3 resolves soul + identity content and renders step 3 (models).
func (h *Handlers) AgentWizardStep3(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Resolve soul content from tab source
	soulContent := resolveSoulContent(r)
	data["SoulContent"] = soulContent

	// Resolve identity content from tab source
	identityContent := resolveIdentityContent(r)
	data["IdentityContent"] = identityContent

	// Populate providers
	data["Providers"] = h.kyvik.ListProviders()

	// When navigating back from step 4+, restore slot fields from JSON
	if slotsJSON := r.FormValue("model_slots_json"); slotsJSON != "" {
		slots, _ := parseSlotJSON(slotsJSON)
		if len(slots) > 0 {
			data["Slots"] = slots
			// Parse routing config for default slot
			var rc router.RoutingConfig
			if rcJSON := r.FormValue("routing_config_json"); rcJSON != "" {
				json.Unmarshal([]byte(rcJSON), &rc)
			}
			data["DefaultSlot"] = rc.DefaultSlot
			data["RoutingConfig"] = rc
			data["AdvancedMode"] = len(slots) > 1 || (len(slots) == 1 && slots[0].Name != "default")
		}
	}

	h.renderFragment(w, r, "wizard-step3", data)
}

// AgentWizardStep4 validates step 3 (model) and renders step 4 (skills).
func (h *Handlers) AgentWizardStep4(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Check if coming from step 3 with indexed slot fields
	if r.FormValue("slot_count") != "" {
		// Parse indexed slot fields from step 3
		slots, defaultSlot, err := parseSlotForm(r)
		if err != nil || len(slots) == 0 {
			data["Error"] = "At least one model slot is required."
			data["Providers"] = h.kyvik.ListProviders()
			h.renderFragment(w, r, "wizard-step3", data)
			return
		}

		// Collect provider names for validation
		providers := h.kyvik.ListProviders()
		providerNames := make([]string, len(providers))
		for i, p := range providers {
			providerNames[i] = p.Name
		}

		// Validate slots
		if errs := validateSlots(slots, defaultSlot, providerNames); len(errs) > 0 {
			data["Error"] = errs[0]
			data["Providers"] = providers
			data["Slots"] = slots
			data["DefaultSlot"] = defaultSlot
			data["AdvancedMode"] = len(slots) > 1 || (len(slots) == 1 && slots[0].Name != "default")
			h.renderFragment(w, r, "wizard-step3", data)
			return
		}

		// Collect routing options from form
		routingOpts := map[string]string{
			"auto_route":      r.FormValue("auto_route"),
			"trigger_prefix":  r.FormValue("trigger_prefix"),
			"classifier_slot": r.FormValue("classifier_slot"),
			"fallback_slot":   r.FormValue("fallback_slot"),
		}

		// Convert to JSON for carry-forward
		slotsJSON, routingJSON, err := slotFormToJSON(slots, defaultSlot, routingOpts)
		if err != nil {
			data["Error"] = "Failed to serialize model configuration."
			data["Providers"] = h.kyvik.ListProviders()
			h.renderFragment(w, r, "wizard-step3", data)
			return
		}
		data["ModelSlotsJSON"] = slotsJSON
		data["RoutingConfigJSON"] = routingJSON

		// Set Provider/Model from the default slot for backward compat
		for _, s := range slots {
			if s.Name == defaultSlot {
				data["Provider"] = s.Provider
				data["Model"] = s.Model
				break
			}
		}
	} else if r.FormValue("model_slots_json") != "" {
		// pass through
	} else {
		if r.FormValue("provider") == "" {
			data["Error"] = "Provider is required."
			data["Providers"] = h.kyvik.ListProviders()
			h.renderFragment(w, r, "wizard-step3", data)
			return
		}
		if r.FormValue("model") == "" {
			data["Error"] = "Model is required."
			data["Providers"] = h.kyvik.ListProviders()
			h.renderFragment(w, r, "wizard-step3", data)
			return
		}
	}

	// Skills catalog
	if sm := h.kyvik.SkillManager(); sm != nil {
		agentCfg := types.AgentConfig{Template: r.FormValue("template")}
		data["AvailableSkills"] = sm.AvailableForAgent(agentCfg)
	}
	selectedSet := make(map[string]bool)
	if raw := r.FormValue("skills_json"); raw != "" {
		var skills []string
		if err := json.Unmarshal([]byte(raw), &skills); err == nil {
			for _, s := range skills {
				selectedSet[s] = true
			}
		}
	}
	data["SelectedSkillsSet"] = selectedSet

	h.renderFragment(w, r, "wizard-step4", data)
}

// AgentWizardStep5 renders step 5 (channels).
func (h *Handlers) AgentWizardStep5(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)
	if r.FormValue("skills_json") == "" {
		if err := r.ParseForm(); err == nil {
			if vals := r.Form["skill_grant"]; len(vals) > 0 {
				if b, err := json.Marshal(vals); err == nil {
					data["SelectedSkillsJSON"] = string(b)
				}
			}
		}
	}
	channelNames := h.kyvik.ListChannelNames()
	data["ChannelNames"] = channelNames
	slackAvailable := false
	discordAvailable := false
	for _, name := range channelNames {
		if name == "slack" {
			slackAvailable = true
		}
		if name == "discord" {
			discordAvailable = true
		}
	}
	data["SlackAvailable"] = slackAvailable
	data["DiscordAvailable"] = discordAvailable
	h.renderFragment(w, r, "wizard-step5", data)
}

// AgentWizardStep6 renders step 6 (tools & permissions).
func (h *Handlers) AgentWizardStep6(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Resolve KTP tier from template
	agentTier := ktp.ResolveAgentTier(r.FormValue("template"))
	data["AgentTier"] = agentTier

	// Get available tools for this tier (nil-safe)
	var availableTools []ktp.ToolDeclaration
	if reg := h.kyvik.KTPRegistry(); reg != nil {
		availableTools = reg.ListForTier(agentTier)
	}
	data["AvailableTools"] = availableTools

	// Build default tool set for pre-checking checkboxes.
	defaultToolSet := make(map[string]bool)
	if reg := h.kyvik.KTPRegistry(); reg != nil {
		for _, name := range reg.DefaultToolsForTier(agentTier) {
			defaultToolSet[name] = true
		}
	}
	data["DefaultToolSet"] = defaultToolSet

	// Restore tool grants if navigating back from step 7+
	if tgJSON := r.FormValue("tool_grants_json"); tgJSON != "" {
		var grants []string
		if err := json.Unmarshal([]byte(tgJSON), &grants); err == nil && len(grants) > 0 {
			toolGrantSet := make(map[string]bool, len(grants))
			for _, g := range grants {
				toolGrantSet[g] = true
			}
			data["ToolGrantSet"] = toolGrantSet
			data["RestrictedTools"] = true
		}
	}

	// Restore capability grants if navigating back
	if cgJSON := r.FormValue("capability_grants_json"); cgJSON != "" {
		var caps []types.Capability
		if err := json.Unmarshal([]byte(cgJSON), &caps); err == nil && len(caps) > 0 {
			data["ExistingCaps"] = caps
		}
	}

	h.renderFragment(w, r, "wizard-step6", data)
}

// AgentWizardStep7 renders step 7 (integrations).
func (h *Handlers) AgentWizardStep7(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Parse tool grants from checkboxes and serialize for carry-forward
	toolGrants := parseToolGrantsForm(r)
	if len(toolGrants) > 0 {
		data["ToolGrantsJSON"] = toolGrantsToJSON(toolGrants)
	}

	// Parse capability overrides and serialize for carry-forward
	caps := parseCapabilityForm(r)
	if len(caps) > 0 {
		data["CapabilityGrantsJSON"] = capabilityGrantsToJSON(caps)
	}

	h.renderFragment(w, r, "wizard-step7", data)
}

// AgentWizardStep8 renders step 8 (scheduling).
func (h *Handlers) AgentWizardStep8(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)
	data["HasScheduler"] = h.kyvik.Lifecycle.Scheduler != nil
	h.renderFragment(w, r, "wizard-step8", data)
}

// AgentWizardStep9 renders step 9 (limits & safety).
func (h *Handlers) AgentWizardStep9(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)
	cfg := types.DefaultCircuitBreakerConfig()
	parseInt := func(raw string, fallback int) int {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v <= 0 {
			return fallback
		}
		return v
	}
	if raw := r.FormValue("circuit_breaker_enabled"); raw != "" {
		cfg.Enabled = raw == "true"
	}
	cfg.ErrorThreshold = parseInt(r.FormValue("circuit_breaker_error_threshold"), cfg.ErrorThreshold)
	cfg.ErrorWindowMinutes = parseInt(r.FormValue("circuit_breaker_error_window_minutes"), cfg.ErrorWindowMinutes)
	cfg.SpendingVelocityPct = parseInt(r.FormValue("circuit_breaker_spending_velocity_pct"), cfg.SpendingVelocityPct)
	cfg.SpendingWindowMinutes = parseInt(r.FormValue("circuit_breaker_spending_window_minutes"), cfg.SpendingWindowMinutes)
	cfg.ActionRatePerMinute = parseInt(r.FormValue("circuit_breaker_action_rate_per_minute"), cfg.ActionRatePerMinute)
	cfg.DestructiveLimit = parseInt(r.FormValue("circuit_breaker_destructive_limit"), cfg.DestructiveLimit)
	cfg.LoopIdenticalCount = parseInt(r.FormValue("circuit_breaker_loop_identical_count"), cfg.LoopIdenticalCount)
	data["BreakerConfig"] = cfg
	h.renderFragment(w, r, "wizard-step9", data)
}

// AgentWizardStep10 renders step 10 (team).
func (h *Handlers) AgentWizardStep10(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)
	if id := r.FormValue("agent_id"); id != "" {
		if tm := h.kyvik.TeamManager(); tm != nil {
			if t, err := tm.GetTeamForAgent(r.Context(), id); err == nil && t != nil {
				data["Team"] = t
			}
		}
	}
	h.renderFragment(w, r, "wizard-step10", data)
}

// AgentWizardQuickReview renders a summary review page for quick-creating an agent from a template.
func (h *Handlers) AgentWizardQuickReview(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)
	data["AdvancedAllowed"] = wizardAdvancedAllowed(r)

	// Validate step 1 basics
	if r.FormValue("name") == "" || r.FormValue("template") == "" {
		if r.FormValue("name") == "" {
			data["Error"] = "Name is required."
		} else {
			data["Error"] = "Permission tier is required."
		}
		templates, err := h.kyvik.ListTemplates(r.Context())
		if err == nil {
			data["Templates"] = templates
			data["TierInfo"] = buildTierInfoMap(h.kyvik.AllowUnrestricted())
		}
		h.renderFragment(w, r, "wizard-step1", data)
		return
	}

	if r.FormValue("from_template_id") == "" {
		data["Error"] = "Quick Create requires a template."
		templates, err := h.kyvik.ListTemplates(r.Context())
		if err == nil {
			data["Templates"] = templates
			data["TierInfo"] = buildTierInfoMap(h.kyvik.AllowUnrestricted())
		}
		h.renderFragment(w, r, "wizard-step1", data)
		return
	}

	// Build summary data for review
	var modelDisplay string
	if slotsJSON := r.FormValue("model_slots_json"); slotsJSON != "" {
		if slots, err := parseSlotJSON(slotsJSON); err == nil && len(slots) > 0 {
			modelDisplay = slots[0].Provider + "/" + slots[0].Model
			if len(slots) > 1 {
				modelDisplay += fmt.Sprintf(" (+%d more)", len(slots)-1)
			}
		}
	}
	if modelDisplay == "" && r.FormValue("provider") != "" {
		modelDisplay = r.FormValue("provider") + "/" + r.FormValue("model")
	}
	data["ModelDisplay"] = modelDisplay

	// Truncate soul/identity for preview
	if sc, _ := data["SoulContent"].(string); len(sc) > 200 {
		data["SoulPreview"] = sc[:200] + "..."
	} else {
		data["SoulPreview"] = data["SoulContent"]
	}
	if ic, _ := data["IdentityContent"].(string); len(ic) > 200 {
		data["IdentityPreview"] = ic[:200] + "..."
	} else {
		data["IdentityPreview"] = data["IdentityContent"]
	}

	h.renderFragment(w, r, "wizard-quick-review", data)
}

// AgentCreate handles the final form submission to create and start an agent.
func (h *Handlers) AgentCreate(w http.ResponseWriter, r *http.Request) {
	data := wizardData(r)

	// Final validation
	if r.FormValue("name") == "" || r.FormValue("provider") == "" || r.FormValue("model") == "" || r.FormValue("template") == "" {
		data["Error"] = "Missing required fields. Please go back and complete all steps."
		h.renderFragment(w, r, "wizard-step10", data)
		return
	}

	// Validate elevated tier confirmation.
	tierConfirm := parseTierConfirmation(r)
	if err := security.ValidateElevatedTier(
		r.FormValue("name"),
		ktp.ResolveAgentTier(r.FormValue("template")),
		"", // no old tier on creation
		tierConfirm,
	); err != nil {
		data["Error"] = "Tier confirmation failed."
		h.renderFragment(w, r, "wizard-step10", data)
		return
	}

	agentID := uuid.New().String()

	config := buildConfigFromRequest(r)
	config.ID = agentID
	config.CreatedAt = timeutil.NowUTC()
	config.UpdatedAt = timeutil.NowUTC()

	if err := h.finalizeAgentCreate(r.Context(), r, &config); err != nil {
		data["Error"] = err.Error()
		if strings.HasPrefix(err.Error(), "Forbidden") {
			w.WriteHeader(http.StatusForbidden)
		}
		h.renderFragment(w, r, "wizard-step10", data)
		return
	}

	w.Header().Set("HX-Redirect", "/agents")
	w.WriteHeader(http.StatusOK)
}

// finalizeAgentCreate handles all post-buildConfigFromRequest processing for agent creation:
// skills parsing, integrations, schedule drafts, tool/capability grants, heartbeat config,
// host paths, vault credentials, template validation, starting the agent, group assignment,
// skill granting, and schedule registration.
func (h *Handlers) finalizeAgentCreate(ctx context.Context, r *http.Request, config *types.AgentConfig) error {
	// Parse selected skills.
	var selectedSkills []string
	if skillsJSON := r.FormValue("skills_json"); skillsJSON != "" {
		_ = json.Unmarshal([]byte(skillsJSON), &selectedSkills)
	}

	// Parse integrations metadata.
	var integrations map[string]any
	if raw := strings.TrimSpace(r.FormValue("integrations_json")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &integrations); err != nil {
			return fmt.Errorf("Invalid integrations configuration.")
		}
	}

	// Parse schedule drafts for create mode.
	type scheduleDraft struct {
		Name     string `json:"name"`
		CronExpr string `json:"cron_expr"`
		Message  string `json:"message"`
		Channel  string `json:"channel"`
	}
	var scheduleDrafts []scheduleDraft
	if raw := strings.TrimSpace(r.FormValue("schedule_drafts_json")); raw != "" {
		_ = json.Unmarshal([]byte(raw), &scheduleDrafts)
	}

	if len(integrations) > 0 {
		if config.Metadata == nil {
			config.Metadata = make(map[string]string)
		}
		raw, _ := json.Marshal(integrations)
		config.Metadata["integrations"] = string(raw)
		if whRaw, ok := integrations["webhooks_inbound"].(map[string]any); ok {
			b, _ := json.Marshal(whRaw)
			var wh types.InboundWebhookConfig
			if err := json.Unmarshal(b, &wh); err == nil {
				config.WebhookInbound = &wh
			}
		}
	}

	// If ModelSlotsJSON is set, also sync ModelConfig from the default slot for backward compat
	if config.ModelSlotsJSON != "" {
		if slots, err := parseSlotJSON(config.ModelSlotsJSON); err == nil && len(slots) > 0 {
			var rc router.RoutingConfig
			if config.RoutingConfigJSON != "" {
				json.Unmarshal([]byte(config.RoutingConfigJSON), &rc)
			}
			defaultName := rc.DefaultSlot
			if defaultName == "" {
				defaultName = "default"
			}
			for _, s := range slots {
				if s.Name == defaultName {
					config.ModelConfig.Provider = s.Provider
					config.ModelConfig.Model = s.Model
					break
				}
			}
		}
	}

	// Parse tool grants and capability grants from JSON hidden fields
	if tgJSON := r.FormValue("tool_grants_json"); tgJSON != "" {
		json.Unmarshal([]byte(tgJSON), &config.ToolGrants)
	}
	if cgJSON := r.FormValue("capability_grants_json"); cgJSON != "" {
		json.Unmarshal([]byte(cgJSON), &config.CapabilityGrants)
	}

	// Build heartbeat config from form fields.
	if r.FormValue("heartbeat_enabled") == "true" {
		hbCfg := types.HeartbeatConfig{
			Enabled:    true,
			Interval:   r.FormValue("heartbeat_interval"),
			Prompt:     r.FormValue("heartbeat_prompt"),
			QuietHours: r.FormValue("heartbeat_quiet_hours"),
		}
		if hbCfg.Interval == "" {
			hbCfg.Interval = "1h"
		}
		// Apply preset if selected.
		if presetID := r.FormValue("heartbeat_preset"); presetID != "" && presetID != "custom" {
			if preset := identity.GetHeartbeatPreset(presetID); preset != nil {
				hbCfg.Prompt = preset.Content
			}
		}
		hbJSON, _ := json.Marshal(hbCfg)
		config.HeartbeatJSON = string(hbJSON)
	}

	// Parse host_paths for power-tier agents.
	hostPaths, err := parseHostPaths(r)
	if err != nil {
		return fmt.Errorf("Invalid host path: %v", err)
	}
	config.HostPaths = hostPaths

	// Store dedicated Slack credentials in vault before starting the agent.
	if config.SlackMode == types.SlackModeDedicated && h.secrets != nil {
		scope := "agent:" + config.ID
		if botToken := r.FormValue("slack_bot_token"); botToken != "" {
			_ = h.secrets.Set(ctx, scope, "slack:bot_token", botToken, "Dedicated Slack bot token")
		}
		if appToken := r.FormValue("slack_app_token"); appToken != "" {
			_ = h.secrets.Set(ctx, scope, "slack:app_token", appToken, "Dedicated Slack app token")
		}
		if signingSecret := r.FormValue("slack_signing_secret"); signingSecret != "" {
			_ = h.secrets.Set(ctx, scope, "slack:signing_secret", signingSecret, "Dedicated Slack signing secret")
		}
	}

	// Store dedicated Discord credentials in vault before starting the agent.
	if config.DiscordMode == types.DiscordModeDedicated && h.secrets != nil {
		scope := "agent:" + config.ID
		if botToken := r.FormValue("discord_bot_token"); botToken != "" {
			_ = h.secrets.Set(ctx, scope, "discord:bot_token", botToken, "Dedicated Discord bot token")
		}
	}

	// If created from a template, validate overrides against locked/constrained fields.
	if templateID := r.FormValue("from_template_id"); templateID != "" && h.templateSvc != nil {
		tmpl, err := h.templateSvc.Get(ctx, templateID)
		if err != nil {
			return fmt.Errorf("Template not found.")
		}

		// Non-admin users must have a role in the template's group.
		if tmpl.GroupID != "" && h.userSvc != nil {
			if u, ok := currentDashboardUser(ctx); ok && !u.IsAdmin {
				roles, _ := h.userSvc.UserGroupRoles(ctx, u.ID)
				hasAccess := false
				for _, gr := range roles {
					if gr.GroupID == tmpl.GroupID {
						hasAccess = true
						break
					}
				}
				if !hasAccess {
					return fmt.Errorf("Forbidden: no access to template group")
				}
			}
		}

		overrides := map[string]any{
			"name":                 config.Name,
			"description":          config.Description,
			"template":             config.Template,
			"max_tokens_per_day":   config.Limits.MaxTokensPerDay,
			"max_tokens_per_month": config.Limits.MaxTokensPerMonth,
			"max_spend_per_day":    config.Limits.MaxSpendPerDay,
			"max_spend_per_month":  config.Limits.MaxSpendPerMonth,
		}
		if err := templates.ValidateOverrides(tmpl, overrides); err != nil {
			return fmt.Errorf("Template constraint violation.")
		}

		// Audit log the template usage.
		if al := h.kyvik.Audit(); al != nil {
			al.Log(ctx, types.AuditEntry{
				AgentID:   config.ID,
				EventType: "template",
				Action:    "agent.created_from_template",
				Details:   fmt.Sprintf("Created from template %q (%s)", tmpl.Name, tmpl.ID),
				Decision:  "allowed",
				RiskLevel: "low",
			})

			// Log override details when constrained fields are overridden.
			if len(tmpl.ConstrainedFields) > 0 {
				var overridden []string
				for field := range tmpl.ConstrainedFields {
					if v, ok := overrides[field]; ok && v != nil {
						overridden = append(overridden, field)
					}
				}
				if len(overridden) > 0 {
					sort.Strings(overridden)
					al.Log(ctx, types.AuditEntry{
						AgentID:   config.ID,
						EventType: "template",
						Action:    "template.constraint_override",
						Details:   fmt.Sprintf("Overrode constrained fields: %s", strings.Join(overridden, ", ")),
						Decision:  "allowed",
						RiskLevel: "medium",
					})
				}
			}
		}
	}

	if err := h.kyvik.StartAgent(ctx, *config); err != nil {
		return fmt.Errorf("Failed to create agent.")
	}

	// Auto-assign agent to the template's group when created from a template.
	if templateID := r.FormValue("from_template_id"); templateID != "" && h.templateSvc != nil && h.userSvc != nil {
		if tmpl, err := h.templateSvc.Get(ctx, templateID); err == nil && tmpl.GroupID != "" {
			_ = h.userSvc.AddAgentToGroup(ctx, tmpl.GroupID, config.ID)
		}
	}

	// Grant selected skills.
	if sm := h.kyvik.SkillManager(); sm != nil && len(selectedSkills) > 0 {
		for _, skillName := range selectedSkills {
			if err := sm.Grant(ctx, config.ID, skillName, "dashboard", *config); err != nil {
				return fmt.Errorf("Failed to grant skill.")
			}
		}
	}

	// Register schedule drafts.
	if sched := h.kyvik.Lifecycle.Scheduler; sched != nil && len(scheduleDrafts) > 0 {
		now := timeutil.NowUTC()
		for _, draft := range scheduleDrafts {
			if strings.TrimSpace(draft.Name) == "" || strings.TrimSpace(draft.CronExpr) == "" || strings.TrimSpace(draft.Message) == "" {
				continue
			}
			newSched := types.Schedule{
				ID:        uuid.NewString(),
				AgentID:   config.ID,
				Name:      draft.Name,
				CronExpr:  draft.CronExpr,
				Message:   draft.Message,
				Channel:   draft.Channel,
				Type:      types.ScheduleTypeTask,
				Enabled:   true,
				Timezone:  sched.DefaultTimezone(),
				CreatedAt: now,
				UpdatedAt: now,
			}
			_ = sched.Add(ctx, newSched)
		}
	}

	return nil
}

// AgentDelete handles agent deletion (POST).
func (h *Handlers) AgentDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if guide.IsGuideAgent(id) {
		if u, ok := currentDashboardUser(r.Context()); !ok || !u.IsAdmin {
			http.Error(w, "only admins can delete the guide agent", http.StatusForbidden)
			return
		}
	}

	if err := h.kyvik.DeleteAgent(r.Context(), id); err != nil {
		h.serverError(w, r, "deleting agent", err)
		return
	}

	w.Header().Set("HX-Redirect", "/agents")
	w.WriteHeader(http.StatusOK)
}

// AgentStart starts a stopped agent.
func (h *Handlers) AgentStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	// ResumeAgent is idempotent-ish: if already running, it returns an error we can ignore for UX.
	_ = h.kyvik.ResumeAgent(ctx, id)

	// Re-render the agent card/row
	h.renderAgentFragment(w, r, id)
}

// AgentStop stops a running agent.
func (h *Handlers) AgentStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if guide.IsGuideAgent(id) {
		if u, ok := currentDashboardUser(ctx); !ok || !u.IsAdmin {
			http.Error(w, "only admins can stop the guide agent", http.StatusForbidden)
			return
		}
	}

	_ = h.kyvik.StopAgent(ctx, id)

	h.renderAgentFragment(w, r, id)
}

// AgentKill immediately terminates an agent.
func (h *Handlers) AgentKill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if guide.IsGuideAgent(id) {
		if u, ok := currentDashboardUser(r.Context()); !ok || !u.IsAdmin {
			http.Error(w, "only admins can kill the guide agent", http.StatusForbidden)
			return
		}
	}

	if err := h.kyvik.KillAgent(r.Context(), id); err != nil {
		h.serverError(w, r, "killing agent", err)
		return
	}

	h.renderAgentFragment(w, r, id)
}

// AgentQuarantine puts an agent into quarantine mode.
func (h *Handlers) AgentQuarantine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if guide.IsGuideAgent(id) {
		if u, ok := currentDashboardUser(ctx); !ok || !u.IsAdmin {
			http.Error(w, "only admins can quarantine the guide agent", http.StatusForbidden)
			return
		}
	}

	if err := h.kyvik.QuarantineAgent(ctx, id); err != nil {
		h.serverError(w, r, "quarantining agent", err)
		return
	}

	h.renderAgentFragment(w, r, id)
}

// KillAll kills all running agents and sets emergency stop.
func (h *Handlers) KillAll(w http.ResponseWriter, r *http.Request) {
	if err := h.kyvik.KillAll(r.Context()); err != nil {
		h.serverError(w, r, "killing all agents", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
}

// ClearEmergencyStop clears the emergency stop flag.
func (h *Handlers) ClearEmergencyStop(w http.ResponseWriter, r *http.Request) {
	if err := h.kyvik.ClearEmergencyStop(r.Context()); err != nil {
		h.serverError(w, r, "clearing emergency stop", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
}

// AgentStatusFragment renders just the status badge for an agent.
func (h *Handlers) AgentStatusFragment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, _ := h.kyvik.GetAgentStatus(r.Context(), id)

	h.renderFragment(w, r, "agent-status", map[string]any{
		"ID":     id,
		"Status": status,
	})
}

// AgentSoulsFragment renders a select list of agents with non-empty SoulContent.
func (h *Handlers) AgentSoulsFragment(w http.ResponseWriter, r *http.Request) {
	agents, err := h.kyvik.ListAgents(r.Context())
	if err != nil {
		h.renderFragment(w, r, "soul-existing", map[string]any{"Agents": nil})
		return
	}

	var withSoul []types.AgentConfig
	for _, a := range agents {
		if a.SoulContent != "" {
			withSoul = append(withSoul, a)
		}
	}
	h.renderFragment(w, r, "soul-existing", map[string]any{"Agents": withSoul})
}

// AgentIdentitiesFragment renders a select list of agents with non-empty IdentityContent.
func (h *Handlers) AgentIdentitiesFragment(w http.ResponseWriter, r *http.Request) {
	agents, err := h.kyvik.ListAgents(r.Context())
	if err != nil {
		h.renderFragment(w, r, "identity-existing", map[string]any{"Agents": nil})
		return
	}

	var withIdentity []types.AgentConfig
	for _, a := range agents {
		if a.IdentityContent != "" {
			withIdentity = append(withIdentity, a)
		}
	}
	h.renderFragment(w, r, "identity-existing", map[string]any{"Agents": withIdentity})
}

// AgentHistory renders the conversation history page for an agent.
func (h *Handlers) AgentHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	channelFilter := r.URL.Query().Get("channel")
	searchQuery := r.URL.Query().Get("q")

	data := map[string]any{
		"Nav":           "agents",
		"Title":         config.Name + " — History",
		"Agent":         config,
		"AgentID":       id,
		"ChannelFilter": channelFilter,
		"SearchQuery":   searchQuery,
		"Entries":       []history.HistoryEntry{},
		"HasMore":       false,
		"NextOffset":    0,
	}

	hs := h.kyvik.Storage.History
	if hs == nil {
		h.renderPageWithRequest(w, r, "agent-history", data)
		return
	}

	pageSize := 50
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if searchQuery != "" {
		entries, err := hs.Search(ctx, id, searchQuery, pageSize+1)
		if err == nil {
			if len(entries) > pageSize {
				data["HasMore"] = true
				entries = entries[:pageSize]
			}
			data["Entries"] = entries
		}
	} else if channelFilter != "" {
		// For channel filter, get all entries for that channel
		entries, err := h.loadHistoryPage(ctx, hs, id, channelFilter, pageSize, offset)
		if err == nil {
			data["Entries"] = entries["Entries"]
			data["HasMore"] = entries["HasMore"]
			data["NextOffset"] = entries["NextOffset"]
		}
	} else {
		// All channels — load from all channels
		entries, err := h.loadHistoryPage(ctx, hs, id, "", pageSize, offset)
		if err == nil {
			data["Entries"] = entries["Entries"]
			data["HasMore"] = entries["HasMore"]
			data["NextOffset"] = entries["NextOffset"]
		}
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "agent-history", data)
		return
	}
	h.renderPageWithRequest(w, r, "agent-history", data)
}

// AgentHistoryFragment returns additional history entries for pagination.
func (h *Handlers) AgentHistoryFragment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	_, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	hs := h.kyvik.Storage.History
	if hs == nil {
		h.renderFragment(w, r, "history-entries", map[string]any{
			"Entries": []history.HistoryEntry{}, "HasMore": false, "Agent": nil,
		})
		return
	}

	channelFilter := r.URL.Query().Get("channel")
	pageSize := 50
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	data, _ := h.loadHistoryPage(ctx, hs, id, channelFilter, pageSize, offset)
	data["AgentID"] = id
	data["ChannelFilter"] = channelFilter
	h.renderFragment(w, r, "history-entries", data)
}

// AgentHistoryClear clears all history for an agent.
func (h *Handlers) AgentHistoryClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	hs := h.kyvik.Storage.History
	if hs != nil {
		_ = hs.Clear(r.Context(), id)
	}

	w.Header().Set("HX-Redirect", "/agents/"+id+"/history")
	w.WriteHeader(http.StatusOK)
}

// loadHistoryPage loads a page of history entries using the all-entries approach
// (simpler query for broad channel search).
func (h *Handlers) loadHistoryPage(ctx context.Context, hs history.HistoryStore, agentID, channel string, pageSize, offset int) (map[string]any, error) {
	result := map[string]any{
		"Entries":    []history.HistoryEntry{},
		"HasMore":    false,
		"NextOffset": 0,
	}

	if channel == "" {
		channel = "webui"
	}
	channelID := ""

	// Load one extra to detect "has more"
	entries, err := hs.Recent(ctx, agentID, channel, channelID, pageSize+offset+1)
	if err != nil {
		return result, err
	}

	// Apply offset by skipping from the beginning (entries are oldest-first)
	if offset > 0 && offset < len(entries) {
		entries = entries[offset:]
	} else if offset >= len(entries) {
		entries = nil
	}

	if len(entries) > pageSize {
		result["HasMore"] = true
		result["NextOffset"] = offset + pageSize
		entries = entries[:pageSize]
	}

	result["Entries"] = entries
	return result, nil
}

// renderAgentFragment renders a card or table row for a single agent.
func (h *Handlers) renderAgentFragment(w http.ResponseWriter, r *http.Request, agentID string) {
	ctx := r.Context()

	// If the request came from an agent detail page, force a full page reload
	// rather than returning a fragment that would replace the page incorrectly.
	referer := r.Header.Get("HX-Current-URL")
	if isAgentDetailURL(referer) {
		w.Header().Set("HX-Refresh", "true")
		return
	}

	config, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	status, _ := h.kyvik.GetAgentStatus(ctx, agentID)

	card := AgentCard{AgentConfig: *config, Status: status}
	type agentCardFragment struct {
		AgentCard
		User map[string]any
	}
	cardView := agentCardFragment{
		AgentCard: card,
		User:      h.templateUser(ctx),
	}

	// Determine which fragment to render based on referer path
	if containsPath(referer, "/agents") {
		h.renderFragment(w, r, "agent-row", cardView)
	} else {
		// Dashboard card — re-render the full card
		h.renderFragment(w, r, "agent-cards", map[string]any{
			"Agents": []agentCardFragment{cardView},
			"User":   cardView.User,
		})
	}
}

// containsPath checks if a URL string contains the given path.
func containsPath(url, path string) bool {
	for i := 0; i <= len(url)-len(path); i++ {
		if url[i:i+len(path)] == path {
			return true
		}
	}
	return false
}

// isAgentDetailURL checks if a URL points to an agent detail page (/agents/{id}).
func isAgentDetailURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && parts[0] == "agents" && parts[1] != ""
}

// resolveSoulContent determines soul content from the tab source.
func resolveSoulContent(r *http.Request) string {
	switch r.FormValue("soul_tab") {
	case "presets":
		if preset := identity.GetSoulPreset(r.FormValue("soul_preset")); preset != nil {
			return preset.Content
		}
		return ""
	case "custom":
		return r.FormValue("soul_custom")
	case "existing":
		return r.FormValue("soul_existing")
	default:
		// Fallback: check if soul_content was already resolved (carried via hidden field)
		return r.FormValue("soul_content")
	}
}

// resolveIdentityContent determines identity content from the tab source.
func resolveIdentityContent(r *http.Request) string {
	switch r.FormValue("identity_tab") {
	case "presets":
		if tmpl := identity.GetRoleTemplate(r.FormValue("identity_preset")); tmpl != nil {
			return tmpl.Content
		}
		return ""
	case "custom":
		return r.FormValue("identity_custom")
	case "existing":
		return r.FormValue("identity_existing")
	default:
		// Fallback: check if identity_content was already resolved (carried via hidden field)
		return r.FormValue("identity_content")
	}
}

// AgentSlotRow renders a single slot row fragment for HTMX dynamic addition.
func (h *Handlers) AgentSlotRow(w http.ResponseWriter, r *http.Request) {
	idx, _ := strconv.Atoi(r.FormValue("slot_count"))
	data := map[string]any{
		"Idx":       idx,
		"Providers": h.kyvik.ListProviders(),
	}
	h.renderFragment(w, r, "slot-row", data)
}

// AgentCapRow renders a single capability row fragment for HTMX dynamic addition.
func (h *Handlers) AgentCapRow(w http.ResponseWriter, r *http.Request) {
	idx, _ := strconv.Atoi(r.FormValue("cap_count"))
	data := map[string]any{
		"Idx": idx,
	}
	h.renderFragment(w, r, "cap-row", data)
}

// parseToolGrantsForm reads tool_grants checkbox values from the edit form.
func parseToolGrantsForm(r *http.Request) []string {
	if err := r.ParseForm(); err != nil {
		return nil
	}
	return r.Form["tool_grants"]
}

// parseCapabilityForm reads indexed capability fields from the form.
func parseCapabilityForm(r *http.Request) []types.Capability {
	countStr := r.FormValue("cap_count")
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		return nil
	}

	var caps []types.Capability
	for i := range count {
		capTool := r.FormValue(fmt.Sprintf("cap_tool_%d", i))
		capAction := r.FormValue(fmt.Sprintf("cap_action_%d", i))
		capResource := r.FormValue(fmt.Sprintf("cap_resource_%d", i))
		// Skip empty rows
		if capTool == "" && capAction == "" && capResource == "" {
			continue
		}
		caps = append(caps, types.Capability{
			Tool:     capTool,
			Action:   capAction,
			Resource: capResource,
		})
	}
	return caps
}

// toolGrantsToJSON serializes a list of tool grant names to JSON.
func toolGrantsToJSON(grants []string) string {
	b, err := json.Marshal(grants)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// capabilityGrantsToJSON serializes capability grants to JSON.
func capabilityGrantsToJSON(caps []types.Capability) string {
	b, err := json.Marshal(caps)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseHostPaths extracts and validates host path configuration from form fields.
// Returns nil if no host paths are configured (non-power templates).
func parseHostPaths(r *http.Request) (*types.HostPathConfig, error) {
	readPaths := r.FormValue("host_paths_read")
	writePaths := r.FormValue("host_paths_write")
	denyPaths := r.FormValue("host_paths_deny")

	if readPaths == "" && writePaths == "" && denyPaths == "" {
		return nil, nil
	}

	readSlice, err := validatePaths(splitPaths(readPaths))
	if err != nil {
		return nil, fmt.Errorf("read paths: %w", err)
	}
	writeSlice, err := validatePaths(splitPaths(writePaths))
	if err != nil {
		return nil, fmt.Errorf("write paths: %w", err)
	}
	denySlice, err := validatePaths(splitPaths(denyPaths))
	if err != nil {
		return nil, fmt.Errorf("deny paths: %w", err)
	}

	return &types.HostPathConfig{
		Read:  readSlice,
		Write: writeSlice,
		Deny:  denySlice,
	}, nil
}

func parseListField(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	var out []string
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// parseKeyValueJSON parses a JSON object string into a map[string]string.
// Returns nil for empty/invalid input.
func parseKeyValueJSON(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	m := make(map[string]string)
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// validatePaths cleans, validates, and deduplicates a list of filesystem paths.
func validatePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(paths))
	var result []string
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("path must be absolute: %q", p)
		}
		if strings.Contains(p, "..") {
			return nil, fmt.Errorf("path must not contain '..': %q", p)
		}
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	return result, nil
}

// buildConfigFromRequest parses shared agent/template form fields into an AgentConfig.
// It handles: limits, context budget, workers, channels, allowlists, circuit breaker,
// model config, and identity fields. Caller is responsible for ID, timestamps,
// integrations, schedules, tool grants, skills, and tier confirmation.
func buildConfigFromRequest(r *http.Request) types.AgentConfig {
	// Parse limits
	maxTokensDay, _ := strconv.ParseInt(r.FormValue("max_tokens_per_day"), 10, 64)
	maxTokensMonth, _ := strconv.ParseInt(r.FormValue("max_tokens_per_month"), 10, 64)
	maxSpendDay, _ := strconv.ParseFloat(r.FormValue("max_spend_per_day"), 64)
	maxSpendMonth, _ := strconv.ParseFloat(r.FormValue("max_spend_per_month"), 64)
	historyLimit, _ := strconv.Atoi(r.FormValue("history_limit"))
	if historyLimit <= 0 {
		historyLimit = 50
	}
	memoryLimit, _ := strconv.Atoi(r.FormValue("memory_limit"))
	if memoryLimit <= 0 {
		memoryLimit = 10
	}
	autoExtract := r.FormValue("auto_extract_memories") == "true"
	maxMemories, _ := strconv.Atoi(r.FormValue("max_memories"))
	memoryExtractionInterval, _ := strconv.Atoi(r.FormValue("memory_extraction_interval"))
	memoryMaxExtractionsPerRun, _ := strconv.Atoi(r.FormValue("memory_max_extractions_per_run"))
	memoryDuplicateThreshold, _ := strconv.ParseFloat(r.FormValue("memory_duplicate_threshold"), 32)
	memorySimilarThreshold, _ := strconv.ParseFloat(r.FormValue("memory_similar_threshold"), 32)
	timestampMessages := r.FormValue("timestamp_messages") == "true"
	attachmentMaxSizeMB, _ := strconv.Atoi(r.FormValue("attachment_max_size_mb"))

	// Parse context budget
	maxTotalTokens, _ := strconv.Atoi(r.FormValue("max_total_tokens"))
	soulIdentityPct, _ := strconv.Atoi(r.FormValue("soul_identity_pct"))
	skillsPct, _ := strconv.Atoi(r.FormValue("skills_pct"))
	memoriesPct, _ := strconv.Atoi(r.FormValue("memories_pct"))
	historyPct, _ := strconv.Atoi(r.FormValue("history_pct"))

	// Parse worker config
	workersEnabled := r.FormValue("workers_enabled") == "true"
	workersMaxConcurrent, _ := strconv.Atoi(r.FormValue("workers_max_concurrent"))
	workersTTLSeconds, _ := strconv.Atoi(r.FormValue("workers_ttl_seconds"))
	workersModelSlot := r.FormValue("workers_model_slot")

	// Parse channel configuration.
	slackMode := r.FormValue("slack_mode")
	if slackMode == "" {
		slackMode = types.SlackModeNone
	}
	slackChannel := r.FormValue("slack_channel")
	discordMode := r.FormValue("discord_mode")
	if discordMode == "" {
		discordMode = types.DiscordModeNone
	}
	discordChannelID := r.FormValue("discord_channel_id")
	webuiEnabled := r.FormValue("webui_enabled") != "false"

	// Parse allowlists.
	httpAllowedHosts := parseListField(r.FormValue("http_allowed_hosts"))
	shellAllowedCommands := parseListField(r.FormValue("shell_allowed_commands"))

	// Parse internal bus visibility.
	canMessage := parseListField(r.FormValue("can_message"))

	// Parse circuit breaker configuration.
	parseIntOrDefault := func(raw string, fallback int) int {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v <= 0 {
			return fallback
		}
		return v
	}
	breakerDefaults := types.DefaultCircuitBreakerConfig()
	breakerEnabled := true
	if raw := r.FormValue("circuit_breaker_enabled"); raw != "" {
		breakerEnabled = raw == "true"
	}
	breakerCfg := types.CircuitBreakerConfig{
		Enabled:               breakerEnabled,
		ErrorThreshold:        parseIntOrDefault(r.FormValue("circuit_breaker_error_threshold"), breakerDefaults.ErrorThreshold),
		ErrorWindowMinutes:    parseIntOrDefault(r.FormValue("circuit_breaker_error_window_minutes"), breakerDefaults.ErrorWindowMinutes),
		SpendingVelocityPct:   parseIntOrDefault(r.FormValue("circuit_breaker_spending_velocity_pct"), breakerDefaults.SpendingVelocityPct),
		SpendingWindowMinutes: parseIntOrDefault(r.FormValue("circuit_breaker_spending_window_minutes"), breakerDefaults.SpendingWindowMinutes),
		ActionRatePerMinute:   parseIntOrDefault(r.FormValue("circuit_breaker_action_rate_per_minute"), breakerDefaults.ActionRatePerMinute),
		DestructiveLimit:      parseIntOrDefault(r.FormValue("circuit_breaker_destructive_limit"), breakerDefaults.DestructiveLimit),
		LoopIdenticalCount:    parseIntOrDefault(r.FormValue("circuit_breaker_loop_identical_count"), breakerDefaults.LoopIdenticalCount),
	}
	breakerJSON, _ := json.Marshal(breakerCfg)

	// Build legacy channel mappings for backward compatibility.
	var channelMappings []types.ChannelMapping
	if slackMode == types.SlackModePrimary && slackChannel != "" {
		channelMappings = append(channelMappings, types.ChannelMapping{
			ChannelType: "slack",
			ChannelID:   slackChannel,
		})
	}
	if discordMode == types.DiscordModePrimary && discordChannelID != "" {
		channelMappings = append(channelMappings, types.ChannelMapping{
			ChannelType: "discord",
			ChannelID:   discordChannelID,
		})
	}

	return types.AgentConfig{
		Name:            r.FormValue("name"),
		Description:     r.FormValue("description"),
		SystemPrompt:    r.FormValue("system_prompt"),
		SoulContent:     r.FormValue("soul_content"),
		IdentityContent: r.FormValue("identity_content"),
		ModelConfig: types.ModelConfig{
			Provider: r.FormValue("provider"),
			Model:    r.FormValue("model"),
		},
		ModelSlotsJSON:       r.FormValue("model_slots_json"),
		RoutingConfigJSON:    r.FormValue("routing_config_json"),
		Template:             r.FormValue("template"),
		HistoryLimit:         historyLimit,
		MemoryLimit:          memoryLimit,
		AutoExtractMemories:        autoExtract,
		MaxMemories:                maxMemories,
		MemoryExtractionInterval:   memoryExtractionInterval,
		MemoryMaxExtractionsPerRun: memoryMaxExtractionsPerRun,
		MemoryDuplicateThreshold:   float32(memoryDuplicateThreshold),
		MemorySimilarThreshold:     float32(memorySimilarThreshold),
		TimestampMessages:          timestampMessages,
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  maxTotalTokens,
			SoulIdentityPct: soulIdentityPct,
			SkillsPct:       skillsPct,
			MemoriesPct:     memoriesPct,
			HistoryPct:      historyPct,
		},
		Workers: types.WorkerConfig{
			Enabled:       workersEnabled,
			MaxConcurrent: workersMaxConcurrent,
			TTLSeconds:    workersTTLSeconds,
			ModelSlot:     workersModelSlot,
		},
		Channels:             channelMappings,
		SlackMode:            slackMode,
		SlackChannel:         slackChannel,
		DiscordMode:          discordMode,
		DiscordChannelID:     discordChannelID,
		WebUIEnabled:         webuiEnabled,
		HTTPAllowedHosts:     httpAllowedHosts,
		ShellAllowedCommands: shellAllowedCommands,
		CanMessage:           canMessage,
		Limits: types.SpendingLimits{
			MaxTokensPerDay:   maxTokensDay,
			MaxTokensPerMonth: maxTokensMonth,
			MaxSpendPerDay:    maxSpendDay,
			MaxSpendPerMonth:  maxSpendMonth,
		},
		CircuitBreakerJSON:  string(breakerJSON),
		AttachmentMaxSizeMB: attachmentMaxSizeMB,
	}
}

// parseTierConfirmation extracts tier confirmation fields from the form.
func parseTierConfirmation(r *http.Request) *security.TierConfirmation {
	ack := r.FormValue("tier_acknowledged") == "true"
	name := r.FormValue("tier_confirm_name")
	if !ack && name == "" {
		return nil
	}
	return &security.TierConfirmation{
		Acknowledged:     ack,
		ConfirmationName: name,
	}
}

// splitPaths splits a newline-separated paths string into a slice, trimming whitespace.
func splitPaths(s string) []string {
	if s == "" {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// ---------- Memory Handlers ----------

type memoryFilters struct {
	tab          string
	showArchived bool
	category     string
	source       string
	search       string
	offset       int
}

func parseMemoryFilters(r *http.Request) memoryFilters {
	_ = r.ParseForm()
	tab := strings.TrimSpace(r.FormValue("tab"))
	showArchived := r.FormValue("archived") == "true"
	if tab == "" {
		if showArchived {
			tab = "archived"
		} else {
			tab = "active"
		}
	}
	switch tab {
	case "archived":
		showArchived = true
	case "review":
		showArchived = false
	}
	offset, _ := strconv.Atoi(r.FormValue("offset"))
	return memoryFilters{
		tab:          tab,
		showArchived: showArchived,
		category:     strings.TrimSpace(r.FormValue("category")),
		source:       strings.TrimSpace(r.FormValue("source")),
		search:       strings.TrimSpace(r.FormValue("q")),
		offset:       offset,
	}
}

func (h *Handlers) memoryListData(ctx context.Context, config *types.AgentConfig, agentID string, ms memory.MemoryStore, filters memoryFilters) ([]memory.Memory, map[int64]int, string, bool, int) {
	pageSize := 50
	opts := memory.ListOptions{
		Category:     filters.category,
		Source:       filters.source,
		ArchivedOnly: filters.showArchived,
		Limit:        pageSize + 1,
		Offset:       filters.offset,
	}

	if filters.tab == "review" {
		opts.Status = "candidate"
		opts.ArchivedOnly = false
	}

	allowSearch := filters.search != "" && filters.tab == "active" && !filters.showArchived
	if allowSearch {
		ep := h.kyvik.EmbeddingProvider(config.ModelConfig.Provider)
		if ep == nil {
			return nil, nil, "Semantic search unavailable (embedding provider not configured).", false, 0
		}
		queryEmb, err := ep.Embed(ctx, filters.search)
		if err != nil {
			return nil, nil, "Semantic search failed.", false, 0
		}
		retriever := memory.NewRetriever(ms)
		scored, err := retriever.Retrieve(ctx, agentID, queryEmb, memory.RetrieveOptions{Limit: pageSize})
		if err != nil {
			return nil, nil, "Semantic search failed.", false, 0
		}

		scores := make(map[int64]int, len(scored))
		mems := make([]memory.Memory, 0, len(scored))
		for _, sm := range scored {
			if opts.Category != "" && sm.Category != opts.Category {
				continue
			}
			if opts.Source != "" && sm.Source != opts.Source {
				continue
			}
			if opts.Reviewed != nil && sm.Reviewed != *opts.Reviewed {
				continue
			}
			mems = append(mems, sm.Memory)
			scores[sm.ID] = int(sm.Score * 100)
		}
		return mems, scores, "", false, 0
	}

	mems, err := ms.List(ctx, agentID, opts)
	if err != nil {
		return nil, nil, "Failed to list memories.", false, 0
	}
	hasMore := false
	nextOffset := 0
	if len(mems) > pageSize {
		hasMore = true
		nextOffset = filters.offset + pageSize
		mems = mems[:pageSize]
	}
	return mems, nil, "", hasMore, nextOffset
}

func (h *Handlers) renderMemoryList(w http.ResponseWriter, r *http.Request, config *types.AgentConfig, ms memory.MemoryStore, agentID string) {
	ctx := r.Context()
	filters := parseMemoryFilters(r)
	mems, scores, searchErr, hasMore, nextOffset := h.memoryListData(ctx, config, agentID, ms, filters)
	unreviewedCount := 0
	if ms != nil {
		unreviewed := false
		count, _ := ms.CountFiltered(ctx, agentID, memory.ListOptions{
			Source:       memory.SourceAuto,
			Reviewed:     &unreviewed,
			ArchivedOnly: false,
		})
		unreviewedCount = count
	}

	data := map[string]any{
		"AgentID":         agentID,
		"Category":        filters.category,
		"Source":          filters.source,
		"ShowArchived":    filters.showArchived,
		"Tab":             filters.tab,
		"SearchQuery":     filters.search,
		"SearchError":     searchErr,
		"Memories":        mems,
		"Scores":          scores,
		"HasMore":         hasMore,
		"NextOffset":      nextOffset,
		"UnreviewedCount": unreviewedCount,
	}

	h.injectTemplateUser(ctx, data)
	h.renderFragment(w, r, "memory-table", data)
}

// AgentMemories renders the memory management page for an agent.
func (h *Handlers) AgentMemories(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ms := h.kyvik.Storage.Memory

	filters := parseMemoryFilters(r)

	data := map[string]any{
		"Nav":             "agents",
		"Title":           config.Name + " — Memories",
		"Agent":           config,
		"AgentID":         id,
		"Category":        filters.category,
		"Source":          filters.source,
		"ShowArchived":    filters.showArchived,
		"Tab":             filters.tab,
		"SearchQuery":     filters.search,
		"Memories":        []memory.Memory{},
		"HasMore":         false,
		"NextOffset":      0,
		"Scores":          map[int64]int{},
		"SearchError":     "",
		"UnreviewedCount": 0,
	}

	if ms != nil {
		mems, scores, searchErr, hasMore, nextOffset := h.memoryListData(ctx, config, id, ms, filters)
		data["Memories"] = mems
		data["Scores"] = scores
		data["SearchError"] = searchErr
		data["HasMore"] = hasMore
		data["NextOffset"] = nextOffset

		unreviewed := false
		count, _ := ms.CountFiltered(ctx, id, memory.ListOptions{
			Source:       memory.SourceAuto,
			Reviewed:     &unreviewed,
			ArchivedOnly: false,
		})
		data["UnreviewedCount"] = count
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "agent-memories", data)
		return
	}
	h.renderPageWithRequest(w, r, "agent-memories", data)
}

// AgentMemoriesFragment returns just the memory table fragment for HTMX pagination/filtering.
func (h *Handlers) AgentMemoriesFragment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		filters := parseMemoryFilters(r)
		data := map[string]any{
			"AgentID":         id,
			"Category":        filters.category,
			"Source":          filters.source,
			"ShowArchived":    filters.showArchived,
			"Tab":             filters.tab,
			"SearchQuery":     filters.search,
			"Memories":        []memory.Memory{},
			"Scores":          map[int64]int{},
			"HasMore":         false,
			"NextOffset":      0,
			"UnreviewedCount": 0,
		}
		h.injectTemplateUser(ctx, data)
		h.renderFragment(w, r, "memory-table", data)
		return
	}

	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoryCreate handles creating a new memory (POST).
func (h *Handlers) AgentMemoryCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	cat := r.FormValue("category")
	content := r.FormValue("content")
	pinned := r.FormValue("pinned") == "on"

	if content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	memID, err := ms.Create(ctx, memory.Memory{
		AgentID:        id,
		Category:       cat,
		Content:        content,
		Source:         memory.SourceUser,
		RelevanceScore: 0.5,
		Pinned:         pinned,
		Reviewed:       true,
	})
	if err != nil {
		h.serverError(w, r, "creating memory", err)
		return
	}

	// Best-effort embed the new memory.
	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent != nil {
		if ep := h.kyvik.EmbeddingProvider(agent.ModelConfig.Provider); ep != nil {
			if vec, err := ep.Embed(ctx, content); err == nil {
				_ = ms.SetEmbedding(ctx, memID, vec, ep.Model())
			}
		}
	}

	agent, _ = h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoryUpdate handles updating an existing memory (POST).
func (h *Handlers) AgentMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	cat := r.FormValue("category")
	content := r.FormValue("content")
	pinned := r.FormValue("pinned") == "on"
	relevance, _ := strconv.ParseFloat(r.FormValue("relevance_score"), 64)
	if relevance <= 0 {
		relevance = 0.5
	}

	existing, err := ms.Get(ctx, memID)
	if err != nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	err = ms.Update(ctx, memory.Memory{
		ID:              memID,
		Content:         content,
		Category:        cat,
		Source:          existing.Source,
		RelevanceScore:  relevance,
		Pinned:          pinned,
		Reviewed:        existing.Reviewed,
		SourceChannel:   existing.SourceChannel,
		SourceChannelID: existing.SourceChannelID,
	})
	if err != nil {
		h.serverError(w, r, "updating memory", err)
		return
	}

	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoryDelete handles deleting a memory (POST).
func (h *Handlers) AgentMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	if err := ms.Delete(ctx, memID); err != nil {
		h.serverError(w, r, "deleting memory", err)
		return
	}

	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoryTogglePin toggles the pinned state of a memory (POST).
func (h *Handlers) AgentMemoryTogglePin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	existing, err := ms.Get(ctx, memID)
	if err != nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	err = ms.Update(ctx, memory.Memory{
		ID:              memID,
		Content:         existing.Content,
		Category:        existing.Category,
		Source:          existing.Source,
		RelevanceScore:  existing.RelevanceScore,
		Pinned:          !existing.Pinned,
		Reviewed:        existing.Reviewed,
		SourceChannel:   existing.SourceChannel,
		SourceChannelID: existing.SourceChannelID,
	})
	if err != nil {
		h.serverError(w, r, "toggling memory pin", err)
		return
	}

	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoryArchive archives a memory (POST).
func (h *Handlers) AgentMemoryArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	if err := ms.Archive(ctx, memID); err != nil {
		h.serverError(w, r, "archiving memory", err)
		return
	}

	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoryUnarchive restores an archived memory (POST).
func (h *Handlers) AgentMemoryUnarchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	if err := ms.Unarchive(ctx, memID); err != nil {
		h.serverError(w, r, "unarchiving memory", err)
		return
	}

	agent, _ := h.kyvik.GetAgent(ctx, id)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, agent, ms, id)
}

// AgentMemoriesClear clears all memories for an agent (POST).
func (h *Handlers) AgentMemoriesClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	ms := h.kyvik.Storage.Memory
	if ms != nil {
		_ = ms.DeleteByAgent(r.Context(), id)
	}

	w.Header().Set("HX-Redirect", "/agents/"+id+"/memories")
	w.WriteHeader(http.StatusOK)
}

type memoryExport struct {
	Category        string  `json:"category"`
	Content         string  `json:"content"`
	Source          string  `json:"source"`
	RelevanceScore  float64 `json:"relevance_score"`
	Pinned          bool    `json:"pinned"`
	Archived        bool    `json:"archived"`
	Reviewed        bool    `json:"reviewed"`
	SourceChannel   string  `json:"source_channel,omitempty"`
	SourceChannelID string  `json:"source_channel_id,omitempty"`
}

type memoryImport struct {
	Category        string   `json:"category"`
	Content         string   `json:"content"`
	Source          string   `json:"source"`
	RelevanceScore  *float64 `json:"relevance_score,omitempty"`
	Pinned          bool     `json:"pinned"`
	Archived        bool     `json:"archived"`
	Reviewed        *bool    `json:"reviewed,omitempty"`
	SourceChannel   string   `json:"source_channel,omitempty"`
	SourceChannelID string   `json:"source_channel_id,omitempty"`
}

func isMemoryCategory(category string) bool {
	switch category {
	case memory.CategoryFact, memory.CategoryDecision, memory.CategoryContext, memory.CategoryInstruction:
		return true
	default:
		return false
	}
}

// AgentMemoriesExport exports all memories for an agent as JSON.
func (h *Handlers) AgentMemoriesExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if _, err := h.kyvik.GetAgent(ctx, id); err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	includeAll := true
	mems, err := ms.List(ctx, id, memory.ListOptions{IncludeArchived: &includeAll})
	if err != nil {
		h.serverError(w, r, "listing memories", err)
		return
	}

	out := make([]memoryExport, 0, len(mems))
	for _, mem := range mems {
		out = append(out, memoryExport{
			Category:        mem.Category,
			Content:         mem.Content,
			Source:          mem.Source,
			RelevanceScore:  mem.RelevanceScore,
			Pinned:          mem.Pinned,
			Archived:        mem.Archived,
			Reviewed:        mem.Reviewed,
			SourceChannel:   mem.SourceChannel,
			SourceChannelID: mem.SourceChannelID,
		})
	}

	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		h.serverError(w, r, "encoding memories", err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "memories-"+id+".json"))
	w.WriteHeader(http.StatusOK)
	w.Write(payload)
}

// AgentMemoriesImport imports memories for an agent from JSON.
func (h *Handlers) AgentMemoriesImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if _, err := h.kyvik.GetAgent(ctx, id); err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var body []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read import", http.StatusBadRequest)
			return
		}
		body = payload
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "failed to parse import", http.StatusBadRequest)
			return
		}
		body = []byte(r.FormValue("payload"))
	}

	var input []memoryImport
	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	memories := make([]memory.Memory, 0, len(input))
	for _, item := range input {
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		category := item.Category
		if !isMemoryCategory(category) {
			category = memory.CategoryFact
		}
		source := strings.TrimSpace(item.Source)
		if source == "" {
			source = memory.SourceUser
		}
		relevance := 0.5
		if item.RelevanceScore != nil && *item.RelevanceScore > 0 {
			relevance = *item.RelevanceScore
		}
		reviewed := true
		if item.Reviewed != nil {
			reviewed = *item.Reviewed
		} else if source == memory.SourceAuto {
			reviewed = false
		}
		memories = append(memories, memory.Memory{
			AgentID:         id,
			Category:        category,
			Content:         item.Content,
			Source:          source,
			RelevanceScore:  relevance,
			Pinned:          item.Pinned,
			Archived:        item.Archived,
			Reviewed:        reviewed,
			SourceChannel:   item.SourceChannel,
			SourceChannelID: item.SourceChannelID,
		})
	}

	if _, err := ms.Import(ctx, id, memories); err != nil {
		h.serverError(w, r, "importing memories", err)
		return
	}

	config, _ := h.kyvik.GetAgent(ctx, id)
	if config == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoriesBulk performs bulk operations on memories.
func (h *Handlers) AgentMemoriesBulk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	_ = r.ParseForm()
	action := strings.TrimSpace(r.FormValue("action"))
	ids := r.Form["memory_id"]
	if len(ids) == 0 {
		http.Error(w, "no memories selected", http.StatusBadRequest)
		return
	}

	parsed := make([]int64, 0, len(ids))
	for _, raw := range ids {
		memID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		parsed = append(parsed, memID)
	}
	if len(parsed) == 0 {
		http.Error(w, "no valid memory IDs", http.StatusBadRequest)
		return
	}

	if action == "delete" {
		u, ok := currentDashboardUser(ctx)
		if !ok || !auth.Can(roleForDashboardUser(u.IsAdmin, u.Role), auth.PermMemoryBulkDelete) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		for _, memID := range parsed {
			_ = ms.Delete(ctx, memID)
		}
		h.renderMemoryList(w, r, config, ms, id)
		return
	}

	switch action {
	case "pin", "unpin", "archive", "unarchive", "category":
	default:
		http.Error(w, "unknown bulk action", http.StatusBadRequest)
		return
	}

	var category string
	if action == "category" {
		category = strings.TrimSpace(r.FormValue("category"))
		if !isMemoryCategory(category) {
			http.Error(w, "invalid category", http.StatusBadRequest)
			return
		}
	}

	for _, memID := range parsed {
		switch action {
		case "archive":
			_ = ms.Archive(ctx, memID)
		case "unarchive":
			_ = ms.Unarchive(ctx, memID)
		default:
			mem, err := ms.Get(ctx, memID)
			if err != nil {
				continue
			}
			switch action {
			case "pin":
				mem.Pinned = true
			case "unpin":
				mem.Pinned = false
			case "category":
				mem.Category = category
			}
			_ = ms.Update(ctx, memory.Memory{
				ID:              mem.ID,
				Content:         mem.Content,
				Category:        mem.Category,
				Source:          mem.Source,
				RelevanceScore:  mem.RelevanceScore,
				Pinned:          mem.Pinned,
				Reviewed:        mem.Reviewed,
				SourceChannel:   mem.SourceChannel,
				SourceChannelID: mem.SourceChannelID,
			})
		}
	}

	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoryReviewApprove marks an auto-extracted memory as reviewed.
func (h *Handlers) AgentMemoryReviewApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}
	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}
	mem, err := ms.Get(ctx, memID)
	if err != nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	mem.Reviewed = true
	if err := ms.Update(ctx, memory.Memory{
		ID:              mem.ID,
		Content:         mem.Content,
		Category:        mem.Category,
		Source:          mem.Source,
		RelevanceScore:  mem.RelevanceScore,
		Pinned:          mem.Pinned,
		Reviewed:        mem.Reviewed,
		SourceChannel:   mem.SourceChannel,
		SourceChannelID: mem.SourceChannelID,
	}); err != nil {
		h.serverError(w, r, "approving memory", err)
		return
	}
	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoryReviewEditApprove edits and approves an auto-extracted memory.
func (h *Handlers) AgentMemoryReviewEditApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}
	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}
	mem, err := ms.Get(ctx, memID)
	if err != nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	content := r.FormValue("content")
	if strings.TrimSpace(content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}
	category := r.FormValue("category")
	if !isMemoryCategory(category) {
		category = memory.CategoryFact
	}

	mem.Content = content
	mem.Category = category
	mem.Reviewed = true
	if err := ms.Update(ctx, memory.Memory{
		ID:              mem.ID,
		Content:         mem.Content,
		Category:        mem.Category,
		Source:          mem.Source,
		RelevanceScore:  mem.RelevanceScore,
		Pinned:          mem.Pinned,
		Reviewed:        mem.Reviewed,
		SourceChannel:   mem.SourceChannel,
		SourceChannelID: mem.SourceChannelID,
	}); err != nil {
		h.serverError(w, r, "approving memory", err)
		return
	}
	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoryReviewReject deletes an auto-extracted memory.
func (h *Handlers) AgentMemoryReviewReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	memIDStr := r.PathValue("memID")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}
	memID, err := strconv.ParseInt(memIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid memory ID", http.StatusBadRequest)
		return
	}
	if err := ms.Delete(ctx, memID); err != nil {
		h.serverError(w, r, "rejecting memory", err)
		return
	}
	h.renderMemoryList(w, r, config, ms, id)
}

// AgentMemoryReviewApproveAll approves all unreviewed auto memories for the agent.
func (h *Handlers) AgentMemoryReviewApproveAll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	ms := h.kyvik.Storage.Memory
	if ms == nil {
		http.Error(w, "memory store not configured", http.StatusInternalServerError)
		return
	}

	unreviewed := false
	mems, err := ms.List(ctx, id, memory.ListOptions{
		Source:       memory.SourceAuto,
		Reviewed:     &unreviewed,
		ArchivedOnly: false,
	})
	if err != nil {
		h.serverError(w, r, "listing memories", err)
		return
	}

	for _, mem := range mems {
		mem.Reviewed = true
		_ = ms.Update(ctx, memory.Memory{
			ID:              mem.ID,
			Content:         mem.Content,
			Category:        mem.Category,
			Source:          mem.Source,
			RelevanceScore:  mem.RelevanceScore,
			Pinned:          mem.Pinned,
			Reviewed:        mem.Reviewed,
			SourceChannel:   mem.SourceChannel,
			SourceChannelID: mem.SourceChannelID,
		})
	}
	h.renderMemoryList(w, r, config, ms, id)
}

// ---------- Agent Queue Handlers ----------

// AgentQueueStats renders the compact queue stats fragment for the agent detail page.
func (h *Handlers) AgentQueueStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	stats := queueStats{}
	if counts, err := q.Stats(ctx, id); err == nil {
		stats.Pending = counts["pending"]
		stats.Processing = counts["processing"]
		stats.Completed = counts["completed"]
		stats.Failed = counts["failed"]
		stats.Total = stats.Pending + stats.Processing + stats.Completed + stats.Failed
	}

	h.renderFragment(w, r, "agent-queue-stats", map[string]any{
		"AgentID": id,
		"Stats":   stats,
	})
}

// AgentQueue renders the full queue view page for an agent.
func (h *Handlers) AgentQueue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	statusFilter := r.URL.Query().Get("status")

	messages, err := q.ListMessages(ctx, id, statusFilter, 100)
	if err != nil {
		h.serverError(w, r, "listing queue messages", err)
		return
	}

	rows := make([]queueMessageRow, 0, len(messages))
	for _, msg := range messages {
		senderName := msg.Sender
		if msg.Sender != "" {
			if cfg, err := h.kyvik.GetAgent(ctx, msg.Sender); err == nil {
				senderName = cfg.Name
			}
		}
		waitTime := ""
		if msg.Status == "pending" {
			waitTime = humanDuration(time.Since(msg.CreatedAt))
		} else if msg.StartedAt != nil && msg.Status == "processing" {
			waitTime = humanDuration(time.Since(*msg.StartedAt))
		}
		rows = append(rows, queueMessageRow{
			ID:          msg.ID,
			DisplayID:   strconv.FormatInt(msg.ID, 10),
			AgentID:     msg.AgentID,
			AgentName:   config.Name,
			Channel:     msg.Channel,
			Sender:      msg.Sender,
			SenderName:  senderName,
			Content:     msg.Content,
			Preview:     previewText(msg.Content, 120),
			MessageType: msg.MessageType,
			Status:      msg.Status,
			Attempts:    msg.Attempts,
			MaxAttempts: msg.MaxAttempts,
			CreatedAt:   msg.CreatedAt,
			StartedAt:   msg.StartedAt,
			CompletedAt: msg.CompletedAt,
			WaitTime:    waitTime,
			ShowActions: true,
		})
	}

	stats := queueStats{}
	if counts, err := q.Stats(ctx, id); err == nil {
		stats.Pending = counts["pending"]
		stats.Processing = counts["processing"]
		stats.Completed = counts["completed"]
		stats.Failed = counts["failed"]
		stats.Total = stats.Pending + stats.Processing + stats.Completed + stats.Failed
	}

	data := map[string]any{
		"Nav":          "agents",
		"Title":        config.Name + " Queue",
		"AgentID":      id,
		"AgentName":    config.Name,
		"Messages":     rows,
		"Stats":        stats,
		"StatusFilter": statusFilter,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "agent-queue", data)
		return
	}
	h.renderPageWithRequest(w, r, "agent-queue", data)
}

// AgentQueueReplayPreview renders pending messages that will be replayed.
func (h *Handlers) AgentQueueReplayPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	statusFilter := "pending"
	messages, err := q.ListMessages(ctx, id, statusFilter, 200)
	if err != nil {
		h.serverError(w, r, "listing queue messages for replay preview", err)
		return
	}

	rows := make([]queueMessageRow, 0, len(messages))
	for _, msg := range messages {
		senderName := msg.Sender
		if msg.Sender != "" {
			if cfg, err := h.kyvik.GetAgent(ctx, msg.Sender); err == nil {
				senderName = cfg.Name
			}
		}
		waitTime := ""
		if msg.Status == "pending" {
			waitTime = humanDuration(time.Since(msg.CreatedAt))
		}
		rows = append(rows, queueMessageRow{
			ID:          msg.ID,
			DisplayID:   strconv.FormatInt(msg.ID, 10),
			AgentID:     msg.AgentID,
			AgentName:   config.Name,
			Channel:     msg.Channel,
			Sender:      msg.Sender,
			SenderName:  senderName,
			Content:     msg.Content,
			Preview:     previewText(msg.Content, 120),
			MessageType: msg.MessageType,
			Status:      msg.Status,
			Attempts:    msg.Attempts,
			MaxAttempts: msg.MaxAttempts,
			CreatedAt:   msg.CreatedAt,
			StartedAt:   msg.StartedAt,
			CompletedAt: msg.CompletedAt,
			WaitTime:    waitTime,
			ShowActions: true,
		})
	}

	stats := queueStats{}
	if counts, err := q.Stats(ctx, id); err == nil {
		stats.Pending = counts["pending"]
		stats.Processing = counts["processing"]
		stats.Completed = counts["completed"]
		stats.Failed = counts["failed"]
		stats.Total = stats.Pending + stats.Processing + stats.Completed + stats.Failed
	}

	if bus := h.kyvik.InternalBus(); bus != nil {
		if pending, err := bus.PendingMessagesFor(ctx, id); err == nil {
			for _, msg := range pending {
				senderName := msg.From
				if msg.From != "" {
					if cfg, err := h.kyvik.GetAgent(ctx, msg.From); err == nil {
						senderName = cfg.Name
					}
				}
				waitTime := ""
				if !msg.Timestamp.IsZero() {
					waitTime = humanDuration(time.Since(msg.Timestamp))
				}
				rows = append(rows, queueMessageRow{
					DisplayID:   msg.ID,
					AgentID:     msg.To,
					AgentName:   config.Name,
					Channel:     "internal",
					Sender:      msg.From,
					SenderName:  senderName,
					Content:     msg.Content,
					Preview:     previewText(msg.Content, 120),
					MessageType: string(msg.Type),
					Status:      "pending",
					CreatedAt:   msg.Timestamp,
					WaitTime:    waitTime,
					ShowActions: false,
				})
			}
			stats.Pending += len(pending)
			stats.Total = stats.Pending + stats.Processing + stats.Completed + stats.Failed
		}
	}

	data := map[string]any{
		"Nav":           "agents",
		"Title":         config.Name + " Queue Replay Preview",
		"AgentID":       id,
		"AgentName":     config.Name,
		"Messages":      rows,
		"Stats":         stats,
		"StatusFilter":  statusFilter,
		"ReplayPreview": true,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "agent-queue", data)
		return
	}
	h.renderPageWithRequest(w, r, "agent-queue", data)
}

// AgentQueueClear deletes queue messages for an agent (optionally by status).
func (h *Handlers) AgentQueueClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	status := strings.TrimSpace(r.FormValue("status"))
	if _, err := h.kyvik.GetAgent(ctx, id); err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if _, err := q.DeleteMessages(ctx, id, status); err != nil {
		h.serverError(w, r, "clearing queue", err)
		return
	}
	if status == "" || status == "pending" {
		if bus := h.kyvik.InternalBus(); bus != nil {
			if _, err := bus.AckPendingMessagesFor(ctx, id); err != nil {
				h.serverError(w, r, "clearing pending internal messages", err)
				return
			}
		}
	}

	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// AgentQueueRetry retries a failed queue message.
func (h *Handlers) AgentQueueRetry(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	if err := q.RetryMessage(r.Context(), msgID); err != nil {
		http.Error(w, "retry failed", http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// AgentQueueDelete deletes a queue message.
func (h *Handlers) AgentQueueDelete(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	if err := q.DeleteMessage(r.Context(), msgID); err != nil {
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// AgentQueueMessageDetail renders the full message detail for a queue message.
func (h *Handlers) AgentQueueMessageDetail(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")

	messages, err := q.ListMessages(r.Context(), agentID, "", 10000)
	if err != nil {
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	var found *queueMessageRow
	for _, msg := range messages {
		if msg.ID == msgID {
			senderName := msg.Sender
			if msg.Sender != "" {
				if cfg, err := h.kyvik.GetAgent(r.Context(), msg.Sender); err == nil {
					senderName = cfg.Name
				}
			}
			found = &queueMessageRow{
				ID:          msg.ID,
				DisplayID:   strconv.FormatInt(msg.ID, 10),
				AgentID:     msg.AgentID,
				Channel:     msg.Channel,
				Sender:      msg.Sender,
				SenderName:  senderName,
				Content:     msg.Content,
				MessageType: msg.MessageType,
				Status:      msg.Status,
				Attempts:    msg.Attempts,
				MaxAttempts: msg.MaxAttempts,
				CreatedAt:   msg.CreatedAt,
				StartedAt:   msg.StartedAt,
				CompletedAt: msg.CompletedAt,
			}
			break
		}
	}
	if found == nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	h.renderFragment(w, r, "agent-queue-message-detail", map[string]any{
		"Message": found,
		"AgentID": agentID,
	})
}

// validEditSections lists the sections that can be edited via per-card modals.
var validEditSections = map[string]bool{
	"identity":        true,
	"model":           true,
	"tools":           true,
	"channels":        true,
	"limits":          true,
	"circuit_breaker": true,
	"workers":         true,
	"heartbeat":       true,
	"webhooks":        true,
	"rest_api":        true,
	"compression":     true,
	"feedback-hooks":  true,
	"cluster":         true,
	"obsidian":        true,
}

// AgentEditSectionGet returns the edit modal HTML for a specific agent config section.
func (h *Handlers) AgentEditSectionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	section := r.PathValue("section")
	ctx := r.Context()

	if !validEditSections[section] {
		http.Error(w, "invalid section", http.StatusBadRequest)
		return
	}

	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	data := h.buildAgentDetailData(ctx, agent)

	// Section-specific data enrichment
	switch section {
	case "model":
		// Parse ModelSlotsJSON into editable form
		var slots []map[string]any
		if agent.ModelSlotsJSON != "" {
			json.Unmarshal([]byte(agent.ModelSlotsJSON), &slots)
		}
		data["ModelSlots"] = slots

		var routingCfg map[string]any
		if agent.RoutingConfigJSON != "" {
			json.Unmarshal([]byte(agent.RoutingConfigJSON), &routingCfg)
		}
		data["RoutingConfigMap"] = routingCfg

	case "tools":
		// SecurityConfig already in data from buildAgentDetailData

	case "channels":
		// SlackAvailable, DiscordAvailable, and AllAgents already in data from buildAgentDetailData
		// Check dedicated Slack credentials
		hasDedicatedCreds := false
		if h.secrets != nil {
			scope := "agent:" + id
			if exists, _ := h.secrets.Exists(ctx, scope, "slack:bot_token"); exists {
				hasDedicatedCreds = true
			}
		}
		data["HasDedicatedCreds"] = hasDedicatedCreds
		// Check dedicated Discord credentials
		hasDedicatedDiscordCreds := false
		if h.secrets != nil {
			scope := "agent:" + id
			if exists, _ := h.secrets.Exists(ctx, scope, "discord:bot_token"); exists {
				hasDedicatedDiscordCreds = true
			}
		}
		data["HasDedicatedDiscordCreds"] = hasDedicatedDiscordCreds

	case "circuit_breaker":
		// BreakerConfig already in data from buildAgentDetailData

	case "heartbeat":
		// HeartbeatConfig and HeartbeatPresets already in data from buildAgentDetailData

	case "rest_api":
		// RESTEndpoints already in data from buildAgentDetailData

	case "compression":
		// CompressionConfig already in data from buildAgentDetailData

	case "feedback-hooks":
		// FeedbackHooksConfig already in data from buildAgentDetailData

	case "obsidian":
		if h.obsidianMgr != nil {
			vaults, _ := h.obsidianMgr.ListVaults(ctx)
			data["AvailableVaults"] = vaults
		}
	}

	templateName := "modal-" + section
	h.renderFragment(w, r, templateName, data)
}

// AgentEditSectionPost saves changes for a specific agent config section.
func (h *Handlers) AgentEditSectionPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	section := r.PathValue("section")
	ctx := r.Context()

	if !validEditSections[section] {
		http.Error(w, "invalid section", http.StatusBadRequest)
		return
	}

	existing, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if guide.IsGuideAgent(id) {
		if u, ok := currentDashboardUser(ctx); !ok || !u.IsAdmin {
			http.Error(w, "only admins can modify the guide agent", http.StatusForbidden)
			return
		}
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	// Copy existing config — we only modify the section's fields.
	config := *existing

	// Helper for parsing int with fallback default.
	parseIntOrDefault := func(raw string, fallback int) int {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v <= 0 {
			return fallback
		}
		return v
	}

	switch section {
	case "identity":
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		config.Name = name
		config.Description = r.FormValue("description")
		config.SoulContent = r.FormValue("soul_content")
		config.IdentityContent = r.FormValue("identity_content")
		config.SystemPrompt = r.FormValue("system_prompt")

	case "model":
		provider := r.FormValue("provider")
		model := r.FormValue("model")

		// Context budget
		config.ContextBudget.MaxTotalTokens, _ = strconv.Atoi(r.FormValue("max_total_tokens"))
		config.ContextBudget.SoulIdentityPct, _ = strconv.Atoi(r.FormValue("soul_identity_pct"))
		config.ContextBudget.SkillsPct, _ = strconv.Atoi(r.FormValue("skills_pct"))
		config.ContextBudget.MemoriesPct, _ = strconv.Atoi(r.FormValue("memories_pct"))
		config.ContextBudget.HistoryPct, _ = strconv.Atoi(r.FormValue("history_pct"))

		// Model slots
		if r.FormValue("slot_count") != "" {
			slots, defaultSlotName, err := parseSlotForm(r)
			if err == nil && len(slots) > 0 {
				routingOpts := map[string]string{
					"auto_route":      r.FormValue("auto_route"),
					"trigger_prefix":  r.FormValue("trigger_prefix"),
					"classifier_slot": r.FormValue("classifier_slot"),
					"fallback_slot":   r.FormValue("fallback_slot"),
				}
				sJSON, rJSON, err := slotFormToJSON(slots, defaultSlotName, routingOpts)
				if err == nil {
					config.ModelSlotsJSON = sJSON
					config.RoutingConfigJSON = rJSON
					// Update provider/model from default slot
					for _, s := range slots {
						if s.Name == defaultSlotName {
							provider = s.Provider
							model = s.Model
							break
						}
					}
				}
			}
		} else {
			// No slots form — clear multi-slot config
			config.ModelSlotsJSON = ""
			config.RoutingConfigJSON = ""
		}

		config.ModelConfig.Provider = provider
		config.ModelConfig.Model = model

	case "tools":
		// Parse permission template
		if tmpl := r.FormValue("template"); tmpl != "" {
			config.Template = tmpl
		}
		// Guide agent must always keep its guide template.
		if guide.IsGuideAgent(id) {
			config.Template = "guide"
		}

		// Parse host paths (admin tier only)
		hostPaths, err := parseHostPaths(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid host path: %v", err), http.StatusBadRequest)
			return
		}
		config.HostPaths = hostPaths

		config.ToolGrants = parseToolGrantsForm(r)
		config.CapabilityGrants = parseCapabilityForm(r)
		config.HTTPAllowedHosts = parseListField(r.FormValue("http_allowed_hosts"))
		config.ShellAllowedCommands = parseListField(r.FormValue("shell_allowed_commands"))

		// Security config
		secCfg := types.SecurityConfig{
			SanitizeExternalContent:     r.FormValue("sec_sanitize") == "true",
			ContentBoundaries:           r.FormValue("sec_boundaries") == "true",
			IdentityReinforcement:       r.FormValue("sec_identity") == "true",
			CanaryTokens:                r.FormValue("sec_canary") == "true",
			OutputValidation:            r.FormValue("sec_output") == "true",
			AnomalyDetectionSensitivity: r.FormValue("sec_anomaly_sensitivity"),
		}
		secJSON, _ := json.Marshal(secCfg)
		config.SecurityJSON = string(secJSON)

	case "channels":
		slackMode := r.FormValue("slack_mode")
		if slackMode == "" {
			slackMode = types.SlackModeNone
		}
		config.SlackMode = slackMode
		config.SlackChannel = r.FormValue("slack_channel")

		discordMode := r.FormValue("discord_mode")
		if discordMode == "" {
			discordMode = types.DiscordModeNone
		}
		config.DiscordMode = discordMode
		config.DiscordChannelID = r.FormValue("discord_channel_id")

		discordAuthMode := r.FormValue("discord_auth_mode")
		if discordAuthMode == "" {
			discordAuthMode = types.DiscordAuthModeOpen
		}
		config.DiscordAuthMode = discordAuthMode

		config.WebUIEnabled = r.FormValue("webui_enabled") == "true"
		config.CanMessage = parseListField(r.FormValue("can_message"))

		// Build legacy channel mappings
		var channelMappings []types.ChannelMapping
		if slackMode == types.SlackModePrimary && config.SlackChannel != "" {
			channelMappings = append(channelMappings, types.ChannelMapping{
				ChannelType: "slack",
				ChannelID:   config.SlackChannel,
			})
		}
		if discordMode == types.DiscordModePrimary && config.DiscordChannelID != "" {
			channelMappings = append(channelMappings, types.ChannelMapping{
				ChannelType: "discord",
				ChannelID:   config.DiscordChannelID,
			})
		}
		config.Channels = channelMappings

		// Store/update dedicated Slack credentials in vault
		if slackMode == types.SlackModeDedicated && h.secrets != nil {
			scope := "agent:" + existing.ID
			if botToken := r.FormValue("slack_bot_token"); botToken != "" {
				_ = h.secrets.Set(ctx, scope, "slack:bot_token", botToken, "Dedicated Slack bot token")
			}
			if appToken := r.FormValue("slack_app_token"); appToken != "" {
				_ = h.secrets.Set(ctx, scope, "slack:app_token", appToken, "Dedicated Slack app token")
			}
			if signingSecret := r.FormValue("slack_signing_secret"); signingSecret != "" {
				_ = h.secrets.Set(ctx, scope, "slack:signing_secret", signingSecret, "Dedicated Slack signing secret")
			}
		}

		// Store/update dedicated Discord credentials in vault
		if discordMode == types.DiscordModeDedicated && h.secrets != nil {
			scope := "agent:" + existing.ID
			if botToken := r.FormValue("discord_bot_token"); botToken != "" {
				_ = h.secrets.Set(ctx, scope, "discord:bot_token", botToken, "Dedicated Discord bot token")
			}
		}

	case "limits":
		config.Limits.MaxTokensPerDay, _ = strconv.ParseInt(r.FormValue("max_tokens_per_day"), 10, 64)
		config.Limits.MaxTokensPerMonth, _ = strconv.ParseInt(r.FormValue("max_tokens_per_month"), 10, 64)
		config.Limits.MaxSpendPerDay, _ = strconv.ParseFloat(r.FormValue("max_spend_per_day"), 64)
		config.Limits.MaxSpendPerMonth, _ = strconv.ParseFloat(r.FormValue("max_spend_per_month"), 64)

		config.MemoryLimit, _ = strconv.Atoi(r.FormValue("memory_limit"))
		if config.MemoryLimit <= 0 {
			config.MemoryLimit = 10
		}
		config.HistoryLimit, _ = strconv.Atoi(r.FormValue("history_limit"))
		if config.HistoryLimit <= 0 {
			config.HistoryLimit = 50
		}
		config.AutoExtractMemories = r.FormValue("auto_extract_memories") == "true"
		config.MaxMemories, _ = strconv.Atoi(r.FormValue("max_memories"))
		config.MemoryExtractionInterval, _ = strconv.Atoi(r.FormValue("memory_extraction_interval"))
		config.MemoryMaxExtractionsPerRun, _ = strconv.Atoi(r.FormValue("memory_max_extractions_per_run"))
		dupThreshold, _ := strconv.ParseFloat(r.FormValue("memory_duplicate_threshold"), 32)
		config.MemoryDuplicateThreshold = float32(dupThreshold)
		simThreshold, _ := strconv.ParseFloat(r.FormValue("memory_similar_threshold"), 32)
		config.MemorySimilarThreshold = float32(simThreshold)
		config.TimestampMessages = r.FormValue("timestamp_messages") == "true"
		config.AttachmentMaxSizeMB, _ = strconv.Atoi(r.FormValue("attachment_max_size_mb"))

	case "circuit_breaker":
		defaults := types.DefaultCircuitBreakerConfig()
		breakerCfg := types.CircuitBreakerConfig{
			Enabled:               r.FormValue("circuit_breaker_enabled") == "true",
			ErrorThreshold:        parseIntOrDefault(r.FormValue("circuit_breaker_error_threshold"), defaults.ErrorThreshold),
			ErrorWindowMinutes:    parseIntOrDefault(r.FormValue("circuit_breaker_error_window_minutes"), defaults.ErrorWindowMinutes),
			SpendingVelocityPct:   parseIntOrDefault(r.FormValue("circuit_breaker_spending_velocity_pct"), defaults.SpendingVelocityPct),
			SpendingWindowMinutes: parseIntOrDefault(r.FormValue("circuit_breaker_spending_window_minutes"), defaults.SpendingWindowMinutes),
			ActionRatePerMinute:   parseIntOrDefault(r.FormValue("circuit_breaker_action_rate_per_minute"), defaults.ActionRatePerMinute),
			DestructiveLimit:      parseIntOrDefault(r.FormValue("circuit_breaker_destructive_limit"), defaults.DestructiveLimit),
			LoopIdenticalCount:    parseIntOrDefault(r.FormValue("circuit_breaker_loop_identical_count"), defaults.LoopIdenticalCount),
		}
		breakerJSON, _ := json.Marshal(breakerCfg)
		config.CircuitBreakerJSON = string(breakerJSON)

	case "workers":
		config.Workers.Enabled = r.FormValue("workers_enabled") == "true"
		config.Workers.MaxConcurrent = parseIntOrDefault(r.FormValue("workers_max_concurrent"), 3)
		config.Workers.TTLSeconds = parseIntOrDefault(r.FormValue("workers_ttl_seconds"), 300)
		config.Workers.ModelSlot = r.FormValue("workers_model_slot")

	case "heartbeat":
		if r.FormValue("heartbeat_enabled") == "true" {
			hbCfg := types.HeartbeatConfig{
				Enabled:    true,
				Interval:   r.FormValue("heartbeat_interval"),
				Prompt:     r.FormValue("heartbeat_prompt"),
				QuietHours: r.FormValue("heartbeat_quiet_hours"),
			}
			if hbCfg.Interval == "" {
				hbCfg.Interval = "1h"
			}
			hbJSON, _ := json.Marshal(hbCfg)
			config.HeartbeatJSON = string(hbJSON)
		} else {
			config.HeartbeatJSON = ""
		}

	case "webhooks":
		if r.FormValue("webhooks_enabled") == "true" {
			wh := types.InboundWebhookConfig{
				Enabled:           true,
				TransformTemplate: r.FormValue("transform_template"),
				AllowedSources:    parseListField(r.FormValue("allowed_sources")),
				SignatureHeader:   r.FormValue("signature_header"),
			}
			wh.RateLimit, _ = strconv.Atoi(r.FormValue("rate_limit"))
			wh.MaxPayloadBytes, _ = strconv.ParseInt(r.FormValue("max_payload_bytes"), 10, 64)
			config.WebhookInbound = &wh
		} else {
			config.WebhookInbound = nil
		}

	case "rest_api":
		countStr := r.FormValue("endpoint_count")
		count, _ := strconv.Atoi(countStr)
		if count > 100 {
			count = 100
		}
		var endpoints []types.RESTAPIEndpoint
		for i := 0; i < count; i++ {
			name := r.FormValue(fmt.Sprintf("endpoint_name_%d", i))
			if name == "" {
				continue
			}
			method := r.FormValue(fmt.Sprintf("endpoint_method_%d", i))
			epURL := r.FormValue(fmt.Sprintf("endpoint_url_%d", i))
			if epURL == "" {
				continue // skip incomplete rows
			}
			if method == "" {
				method = "GET"
			}
			ep := types.RESTAPIEndpoint{
				Name:   name,
				Method: method,
				URL:    epURL,
			}
			if desc := r.FormValue(fmt.Sprintf("endpoint_description_%d", i)); desc != "" {
				ep.Description = desc
			}
			if bodyTmpl := r.FormValue(fmt.Sprintf("endpoint_body_template_%d", i)); bodyTmpl != "" {
				ep.BodyTemplate = bodyTmpl
			}
			if respTmpl := r.FormValue(fmt.Sprintf("endpoint_response_template_%d", i)); respTmpl != "" {
				ep.ResponseTemplate = respTmpl
			}
			if authType := r.FormValue(fmt.Sprintf("endpoint_auth_type_%d", i)); authType != "" && authType != "none" {
				ep.Auth = types.RESTAPIAuth{
					Type:       authType,
					SecretRef:  r.FormValue(fmt.Sprintf("endpoint_auth_secret_%d", i)),
					HeaderName: r.FormValue(fmt.Sprintf("endpoint_auth_header_%d", i)),
				}
			}
			ep.CacheTTLSeconds, _ = strconv.Atoi(r.FormValue(fmt.Sprintf("endpoint_cache_%d", i)))
			ep.RateLimitRPM, _ = strconv.Atoi(r.FormValue(fmt.Sprintf("endpoint_rate_%d", i)))
			ep.TimeoutSeconds, _ = strconv.Atoi(r.FormValue(fmt.Sprintf("endpoint_timeout_%d", i)))
			endpoints = append(endpoints, ep)
		}
		if len(endpoints) > 0 {
			epJSON, _ := json.Marshal(endpoints)
			config.RESTAPIEndpointsJSON = string(epJSON)
		} else {
			config.RESTAPIEndpointsJSON = ""
		}

	case "compression":
		compCfg := types.CompressionConfig{
			Enabled:            r.FormValue("compression_enabled") == "true",
			Model:              strings.TrimSpace(r.FormValue("compression_model")),
			MessageThreshold:   parseIntOrDefault(r.FormValue("compression_message_threshold"), 20),
			TokenThresholdPct:  parseIntOrDefault(r.FormValue("compression_token_threshold_pct"), 70),
			KeepRecentMessages: parseIntOrDefault(r.FormValue("compression_keep_recent"), 10),
		}
		compJSON, _ := json.Marshal(compCfg)
		config.CompressionJSON = string(compJSON)

	case "feedback-hooks":
		var fhCfg types.FeedbackHooksConfig
		fhCfg.Enabled = r.FormValue("feedback_hooks_enabled") == "true"
		fhCfg.MaxOutputBytes = parseIntOrDefault(r.FormValue("feedback_hooks_max_output_bytes"), 4096)
		hooksJSON := strings.TrimSpace(r.FormValue("feedback_hooks_json"))
		if hooksJSON != "" {
			var hooks []types.FeedbackHook
			if err := json.Unmarshal([]byte(hooksJSON), &hooks); err != nil {
				http.Error(w, "invalid hooks JSON", http.StatusBadRequest)
				return
			}
			fhCfg.Hooks = hooks
		}
		fhJSON, _ := json.Marshal(fhCfg)
		config.FeedbackHooksJSON = string(fhJSON)

	case "cluster":
		affinityRaw := strings.TrimSpace(r.FormValue("node_affinity"))
		preferenceRaw := strings.TrimSpace(r.FormValue("node_preference"))
		config.NodeAffinity = parseKeyValueJSON(affinityRaw)
		config.NodePreference = parseKeyValueJSON(preferenceRaw)

	case "obsidian":
		selected := r.Form["obsidian_vaults"]
		config.ObsidianVaults = selected
	}

	config.UpdatedAt = timeutil.NowUTC()

	if err := h.kyvik.UpdateAgent(ctx, config); err != nil {
		h.serverError(w, r, "updating agent settings", err)
		return
	}

	// Sync discord auth mode to the live adapter without restart.
	if section == "channels" && h.channelMgr != nil {
		h.channelMgr.UpdateDiscordAuthMode(id, config.DiscordAuthMode)
	}

	// Reload agent to get canonical state
	updated, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "failed to reload agent after save", http.StatusInternalServerError)
		return
	}

	// Build full template data for the card re-render
	data := h.buildAgentDetailData(ctx, updated)

	templateName := "card-" + section
	h.renderFragment(w, r, templateName, data)
}
