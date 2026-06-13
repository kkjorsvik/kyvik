// Package core implements the Kyvik agent lifecycle and message routing.
// Each agent is managed by a goroutine in the core process.
// Actual tool execution is delegated to sandboxes.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/backup"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/channels/busadapter"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/cluster"
	"github.com/kkjorsvik/kyvik/internal/compression"
	"github.com/kkjorsvik/kyvik/internal/feedback"
	"github.com/kkjorsvik/kyvik/internal/ctxbudget"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/retention"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/sandbox"
	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/integrations"
	obsidianpkg "github.com/kkjorsvik/kyvik/internal/obsidian"
	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/teams"
	tmplsvc "github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/internal/workers"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const (
	defaultMaxToolIterations = 64
	minMaxToolIterations     = 4
	hardMaxToolIterations    = 256
)

// KeyProvisioner provisions and revokes per-agent API keys.
type KeyProvisioner interface {
	ProvisionKey(ctx context.Context, agentID, agentName string, spendLimit float64) error
	RevokeKey(ctx context.Context, agentID string) error
}

// Kyvik is the central runtime that manages all agents and subsystems.
type Kyvik struct {
	store             store.Store
	gate              permissions.Gate
	sandboxMgr        *sandbox.Manager
	audit             audit.Logger
	tools             tools.Registry
	spending          spending.Tracker
	models            map[string]models.Provider
	channels          map[string]channels.Adapter
	agents            map[string]*AgentRunner
	assembler         *ctxbudget.Assembler      // nil = use legacy prompt assembly
	keyProvisioner    KeyProvisioner            // nil = per-agent keys disabled
	workers           *workers.WorkerManager    // ephemeral worker manager (nil = disabled)
	ktpExecutor       *ktp.Executor             // nil = KTP tools disabled
	ktpRegistry       *ktp.Registry             // nil = no KTP tools
	feedbackRunner    *feedback.Runner          // nil = feedback hooks disabled
	workspaceRoot     string                   // base directory for agent workspaces
	backupMgr         *backup.Manager           // nil = backups disabled
	skillManager      *skills.Manager           // nil = skills disabled
	integrationMgr    *integrations.Manager     // nil = integrations disabled
	internalBus       *teams.Bus                // nil = internal messaging disabled
	teamManager       *teams.Manager            // nil = team management disabled
	pairedOrch        *teams.PairedOrchestrator // nil = paired conversations disabled
	templateSvc       *tmplsvc.Service          // nil = agent templates disabled
	obsidianMgr       *obsidianpkg.VaultManager // nil = obsidian vaults disabled
	cluster           cluster.Manager           // nil = single-node mode
	hostAccessMode    string                    // "sandbox" or "host"
	allowUnrestricted bool                      // mirrors config for agent start validation
	mu                sync.RWMutex
	emergencyStop     atomic.Bool // set by KillAll, prevents new agent starts
	vacationMode      atomic.Bool // set by ActivateVacationMode, prevents new agent starts

	routerCtx    context.Context    // context for the message router
	routerCancel context.CancelFunc // cancels the message router

	// Subsystem composites — new groupings for cleaner access
	Storage       StorageSubsystem
	Security      SecuritySubsystem
	Communication CommunicationSubsystem
	Lifecycle     LifecycleSubsystem
}

// AgentRunner manages the lifecycle of a single agent within the core process.
// The provider field holds the default slot's provider, resolved at start time.
type AgentRunner struct {
	config   types.AgentConfig
	provider models.Provider
	cancel   context.CancelFunc
	done     chan struct{}
	// Internal channels for message passing
	inbox        chan types.Message
	outbox       chan types.Message
	messageCount int // counts messages for memory extraction trigger; reset on restart
	// Protects status field
	mu     sync.RWMutex
	status types.AgentStatus

	outboxCancel context.CancelFunc // cancels the outbox consumer goroutine
	outboxDone   chan struct{}      // closed when the outbox consumer exits
}

func (r *AgentRunner) getStatus() types.AgentStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *AgentRunner) setStatus(s types.AgentStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = s
}

// New creates a new Kyvik runtime with the given dependencies.
// sbMgr may be nil if sandbox execution is not yet configured.
func New(
	s store.Store,
	g permissions.Gate,
	sbMgr *sandbox.Manager,
	al audit.Logger,
	tr tools.Registry,
	sp spending.Tracker,
) *Kyvik {
	k := &Kyvik{
		store:      s,
		gate:       g,
		sandboxMgr: sbMgr,
		audit:      al,
		tools:      tr,
		spending:   sp,
		models:     make(map[string]models.Provider),
		channels:   make(map[string]channels.Adapter),
		agents:     make(map[string]*AgentRunner),
	}
	// Populate composites for fields set during construction
	k.Storage.Store = s
	k.Security.Gate = g
	k.Security.Audit = al
	return k
}

// SandboxManager returns the sandbox manager, or nil if not configured.
func (p *Kyvik) SandboxManager() *sandbox.Manager { return p.sandboxMgr }

// Start boots all subsystems and begins serving.
// Sequence: open store → load templates → start channel adapters →
// start message router → resume previously-running agents → start web server.
func (p *Kyvik) Start(ctx context.Context) error {
	slog.Info("kyvik starting")

	if err := p.StartRouter(ctx); err != nil {
		return fmt.Errorf("starting router: %w", err)
	}

	if p.cluster != nil {
		p.cluster.OnAgentAssigned(func(agentID, nodeID string) {
			if nodeID == p.cluster.NodeID() {
				go p.StartLocalAgent(ctx, agentID)
			}
		})
	}

	slog.Info("kyvik started")
	return nil
}

// Shutdown gracefully stops all agents and subsystems.
func (p *Kyvik) Shutdown(ctx context.Context) error {
	slog.Info("kyvik shutting down")

	// Stop cluster manager first to deregister from the cluster
	if p.cluster != nil {
		p.cluster.Stop()
	}

	// Stop the message router first so no new messages arrive
	if p.routerCancel != nil {
		p.routerCancel()
	}

	// Snapshot agent IDs under read lock
	p.mu.RLock()
	ids := make([]string, 0, len(p.agents))
	for id := range p.agents {
		ids = append(ids, id)
	}
	p.mu.RUnlock()

	// Stop each agent, preserving desired state for restart recovery
	var firstErr error
	for _, id := range ids {
		if err := p.stopAgentInternal(ctx, id, true); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Stop ephemeral workers
	if p.workers != nil {
		p.workers.Stop()
	}

	// Stop the persistent queue
	if p.Storage.Queue != nil {
		p.Storage.Queue.Stop()
	}

	// Stop the scheduler
	if p.Lifecycle.Scheduler != nil {
		p.Lifecycle.Scheduler.Stop()
	}

	// Stop the backup manager
	if p.backupMgr != nil {
		p.backupMgr.Stop()
	}

	// Stop the retention pruner
	if p.Lifecycle.Pruner != nil {
		p.Lifecycle.Pruner.Stop()
	}

	// Stop notifier to flush suppression summaries
	if p.Communication.Notifier != nil {
		p.Communication.Notifier.Stop()
	}

	// Close channel adapters
	for _, adapter := range p.channels {
		if err := adapter.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Flush audit logger (drain batched entries before closing store)
	if err := p.audit.Close(); err != nil && firstErr == nil {
		firstErr = err
	}

	// Close store
	if err := p.store.Close(); err != nil && firstErr == nil {
		firstErr = err
	}

	return firstErr
}

// SetQueue sets the persistent message queue. Must be called before Start().
func (p *Kyvik) SetQueue(q queue.Queue) {
	p.Storage.Queue = q
}

// SetHistory sets the conversation history store. Must be called before Start().
func (p *Kyvik) SetHistory(h history.HistoryStore) {
	p.Storage.History = h
}

// SetMemory sets the agent memory store. Must be called before Start().
func (p *Kyvik) SetMemory(m memory.MemoryStore) {
	p.Storage.Memory = m
}

// EmbeddingProvider returns the EmbeddingProvider for the given agent's model provider.
// Returns nil if the provider doesn't support embeddings.
func (p *Kyvik) EmbeddingProvider(providerName string) models.EmbeddingProvider {
	p.mu.RLock()
	provider, ok := p.models[providerName]
	p.mu.RUnlock()
	if !ok {
		return nil
	}
	ep, ok := provider.(models.EmbeddingProvider)
	if !ok {
		return nil
	}
	return ep
}

// Models returns the registered model providers for external iteration.
func (p *Kyvik) Models() map[string]models.Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]models.Provider, len(p.models))
	for k, v := range p.models {
		out[k] = v
	}
	return out
}

// SetKeyProvisioner sets the per-agent key provisioner. Must be called before Start().
func (p *Kyvik) SetKeyProvisioner(kp KeyProvisioner) {
	p.keyProvisioner = kp
}

// SetNotifier configures a notifier for agent error alerts.
func (p *Kyvik) SetNotifier(n notifications.Notifier) {
	p.Communication.Notifier = n
}

// SetWorkerManager sets the ephemeral worker manager. Must be called before Start().
func (p *Kyvik) SetWorkerManager(wm *workers.WorkerManager) {
	p.workers = wm
}

// WorkerManager returns the ephemeral worker manager, or nil if not configured.
func (p *Kyvik) WorkerManager() *workers.WorkerManager {
	return p.workers
}

// SetAssembler sets the context assembler for budget-aware prompt assembly.
func (p *Kyvik) SetAssembler(a *ctxbudget.Assembler) {
	p.assembler = a
}

// Assembler returns the context assembler, or nil if not configured.
func (p *Kyvik) Assembler() *ctxbudget.Assembler {
	return p.assembler
}

// SetRouter sets the unified routing pipeline.
func (p *Kyvik) SetRouter(r *router.Router) {
	p.Communication.Router = r
}

// SetKTPExecutor sets the KTP tool executor. Must be called before Start().
func (p *Kyvik) SetKTPExecutor(e *ktp.Executor) { p.ktpExecutor = e }

// SetKTPRegistry sets the KTP tool registry. Must be called before Start().
func (p *Kyvik) SetKTPRegistry(r *ktp.Registry) { p.ktpRegistry = r }

// KTPRegistry returns the KTP tool registry, or nil if not configured.
func (p *Kyvik) KTPRegistry() *ktp.Registry { return p.ktpRegistry }

// SetSecurity sets the prompt injection defense system. Must be called before Start().
func (p *Kyvik) SetSecurity(s *security.Defense) {
	p.Security.Defense = s
}

// SetConversationStore sets the web conversation metadata store. Must be called before Start().
func (p *Kyvik) SetConversationStore(cs history.ConversationStore) {
	p.Storage.Conversations = cs
}

// SetCircuitBreakerManager sets the circuit breaker manager. Must be called before Start().
func (p *Kyvik) SetCircuitBreakerManager(m *circuitbreaker.Manager) {
	p.Lifecycle.Breaker = m
}

// SetScheduler wires the cron scheduler into the runtime.
func (p *Kyvik) SetScheduler(s *scheduler.Scheduler) {
	p.Lifecycle.Scheduler = s
}

// SetPruner wires the retention pruner into the runtime.
func (p *Kyvik) SetPruner(pr *retention.Pruner) {
	p.Lifecycle.Pruner = pr
}

// SetCompressor wires the conversation compressor into the runtime.
func (p *Kyvik) SetCompressor(c *compression.Compressor) {
	p.Lifecycle.Compressor = c
}

// SetFeedbackRunner wires the feedback hook runner into the runtime.
func (p *Kyvik) SetFeedbackRunner(r *feedback.Runner) { p.feedbackRunner = r }

// SetWorkspaceRoot sets the base directory for agent workspaces.
func (p *Kyvik) SetWorkspaceRoot(root string) { p.workspaceRoot = root }

// WorkspaceRoot returns the base directory for agent workspaces.
func (p *Kyvik) WorkspaceRoot() string { return p.workspaceRoot }

// SetBackupManager wires the backup manager into the runtime.
func (p *Kyvik) SetBackupManager(bm *backup.Manager) { p.backupMgr = bm }

// BackupManager returns the backup manager, or nil if not configured.
func (p *Kyvik) BackupManager() *backup.Manager { return p.backupMgr }

// SetSkillManager wires the skill manager into the runtime.
func (p *Kyvik) SetSkillManager(m *skills.Manager) { p.skillManager = m }

// SkillManager returns the skill manager, or nil if not configured.
func (p *Kyvik) SkillManager() *skills.Manager { return p.skillManager }

// SetIntegrationManager wires the integration manager into the runtime.
func (p *Kyvik) SetIntegrationManager(m *integrations.Manager) { p.integrationMgr = m }

// IntegrationManager returns the integration manager, or nil if not configured.
func (p *Kyvik) IntegrationManager() *integrations.Manager { return p.integrationMgr }

// SetInternalBus wires the internal message bus into the runtime.
func (p *Kyvik) SetInternalBus(b *teams.Bus) { p.internalBus = b }

// InternalBus returns the internal message bus, or nil if not configured.
func (p *Kyvik) InternalBus() *teams.Bus { return p.internalBus }

// SetTeamManager wires the team manager into the runtime.
func (p *Kyvik) SetTeamManager(m *teams.Manager) { p.teamManager = m }

// TeamManager returns the team manager, or nil if not configured.
func (p *Kyvik) TeamManager() *teams.Manager { return p.teamManager }

// SetPairedOrchestrator wires the paired conversation orchestrator into the runtime.
func (p *Kyvik) SetPairedOrchestrator(o *teams.PairedOrchestrator) { p.pairedOrch = o }

// PairedOrchestrator returns the paired conversation orchestrator, or nil if not configured.
func (p *Kyvik) PairedOrchestrator() *teams.PairedOrchestrator { return p.pairedOrch }

// SetTemplateService wires the agent template service into the runtime.
func (p *Kyvik) SetTemplateService(ts *tmplsvc.Service) { p.templateSvc = ts }

// TemplateService returns the template service, or nil if not configured.
func (p *Kyvik) TemplateService() *tmplsvc.Service { return p.templateSvc }

// SetCluster wires the cluster manager into the runtime.
func (p *Kyvik) SetCluster(m cluster.Manager) { p.cluster = m }

// GetCluster returns the cluster manager, or nil if in single-node mode.
func (p *Kyvik) GetCluster() cluster.Manager { return p.cluster }

// SetObsidianVaultManager wires the Obsidian vault manager into the runtime.
func (p *Kyvik) SetObsidianVaultManager(m *obsidianpkg.VaultManager) { p.obsidianMgr = m }

// ObsidianVaultManager returns the Obsidian vault manager, or nil if disabled.
func (p *Kyvik) ObsidianVaultManager() *obsidianpkg.VaultManager { return p.obsidianMgr }

// SetHostAccessConfig configures the host access mode and unrestricted tier toggle.
// Must be called before Start(). mode is "sandbox" or "host".
func (p *Kyvik) SetHostAccessConfig(mode string, allow bool) {
	p.hostAccessMode = mode
	p.allowUnrestricted = allow
}

// HostAccessMode returns the configured host access mode ("sandbox" or "host").
func (p *Kyvik) HostAccessMode() string { return p.hostAccessMode }

// AllowUnrestricted returns whether the unrestricted tier is enabled.
func (p *Kyvik) AllowUnrestricted() bool { return p.allowUnrestricted }

// Audit returns the audit logger.
func (p *Kyvik) Audit() audit.Logger { return p.audit }

// Spending returns the spending tracker.
func (p *Kyvik) Spending() spending.Tracker { return p.spending }

// RegisterModel adds a model provider to the runtime.
func (p *Kyvik) RegisterModel(provider models.Provider) {
	p.models[provider.Name()] = provider
}

// RegisterModelAs registers a provider under a specific instance ID.
// Used by ProviderManager for multi-instance support (e.g. two OpenAI accounts).
func (p *Kyvik) RegisterModelAs(instanceID string, provider models.Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models[instanceID] = provider
}

// UnregisterModel removes a provider by its instance ID.
func (p *Kyvik) UnregisterModel(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.models, instanceID)
}

// RegisterChannel adds a channel adapter to the runtime.
func (p *Kyvik) RegisterChannel(adapter channels.Adapter) {
	p.channels[adapter.Name()] = adapter
}

// StartAgent creates and starts a new agent runner.
func (p *Kyvik) StartAgent(ctx context.Context, config types.AgentConfig) error {
	log := slog.With("agent_id", config.ID)

	// Validate config
	if config.ID == "" {
		return fmt.Errorf("agent ID is required")
	}

	if p.emergencyStop.Load() {
		return fmt.Errorf("emergency stop active: new agent starts are blocked until cleared")
	}

	if p.vacationMode.Load() {
		return fmt.Errorf("%w", types.ErrVacationModeActive)
	}

	// If clustered, check if this agent should run on this node
	if p.cluster != nil && !p.cluster.IsLocalAgent(config.ID) {
		nodeID, err := p.cluster.GetAssignment(config.ID)
		if err != nil || nodeID == "" {
			nodeID, err = p.cluster.RequestAssignment(config.ID)
			if err != nil {
				return fmt.Errorf("cluster assignment: %w", err)
			}
		}
		if nodeID != p.cluster.NodeID() {
			slog.Info("agent assigned to remote node", "agent_id", config.ID, "node_id", nodeID)
			return nil
		}
	}

	// Validate tier against host access config.
	agentTier := ktp.ResolveAgentTier(config.Template)
	if agentTier == ktp.TierAdmin &&
		p.hostAccessMode == "sandbox" && hasHostAccessConfig(config) {
		return fmt.Errorf("agent has host_paths configured but sandbox.host_access=\"sandbox\"; set sandbox.host_access=\"host\" and regenerate the service file (make install-service)")
	}

	// Resolve model slots (backward-compatible: empty ModelSlotsJSON falls back to ModelConfig)
	resolved, err := router.ResolveSlots(config)
	if err != nil {
		return fmt.Errorf("resolving model slots: %w", err)
	}
	if resolved.DefaultSlot.Provider == "" {
		return fmt.Errorf("model provider is required")
	}

	// Lookup provider for the default slot
	provider, ok := p.models[resolved.DefaultSlot.Provider]
	if !ok {
		return fmt.Errorf("%w: %s", types.ErrProviderUnavailable, resolved.DefaultSlot.Provider)
	}

	// Backfill ModelConfig for backward-compatible code paths (embedding, audit, etc.)
	if config.ModelSlotsJSON != "" {
		config.ModelConfig.Provider = resolved.DefaultSlot.Provider
		config.ModelConfig.Model = resolved.DefaultSlot.Model
	}

	// Check agent not already running
	p.mu.Lock()
	if _, exists := p.agents[config.ID]; exists {
		p.mu.Unlock()
		return fmt.Errorf("%w: %s", types.ErrAgentAlreadyRunning, config.ID)
	}

	// Set desired/actual state before persisting
	config.DesiredState = types.DesiredStateRunning
	config.ActualState = types.AgentStatusStarting

	// Persist agent config to store (create or update if already exists).
	if err := p.store.CreateAgent(ctx, config); err != nil {
		if uerr := p.store.UpdateAgent(ctx, config); uerr != nil {
			p.mu.Unlock()
			return fmt.Errorf("persisting agent config: %w", err)
		}
	}

	// Provision per-agent API key if configured
	if p.keyProvisioner != nil && config.ModelConfig.Provider == "openrouter" {
		if err := p.keyProvisioner.ProvisionKey(ctx, config.ID, config.Name, config.Limits.MaxSpendPerMonth); err != nil {
			log.Warn("per-agent key provisioning failed, using shared key", "error", err)
		}
	}

	// Create the runner
	runner := &AgentRunner{
		config:   config,
		provider: provider,
		status:   types.AgentStatusStarting,
		inbox:    make(chan types.Message, 16),
		outbox:   make(chan types.Message, 16),
		done:     make(chan struct{}),
	}

	p.agents[config.ID] = runner
	p.mu.Unlock()

	// Create independent context — agent outlives the HTTP request that started it
	agentCtx, cancel := context.WithCancel(context.Background())
	runner.cancel = cancel

	// Initialize queue channel and replay pending messages for this agent.
	if p.Storage.Queue != nil {
		p.Storage.Queue.Dequeue(ctx, config.ID)
		if err := p.Storage.Queue.ReplayAgent(ctx, config.ID); err != nil {
			log.Warn("queue replay failed for agent", "error", err)
		}

		// Replay bus messages sent while the agent was offline.
		if internalAdapter, ok := p.channels["internal"]; ok {
			if ba, ok := internalAdapter.(*busadapter.Adapter); ok {
				if err := ba.ReplayUndelivered(ctx, config.ID, p.Storage.Queue); err != nil {
					log.Warn("bus message replay failed", "error", err)
				}
			}
		}
	}

	// Launch agent goroutine
	go p.runAgent(agentCtx, runner)

	// Provision channel adapters that match the agent's channel config
	p.provisionChannels(ctx, config, log)

	// Start outbox consumer only when channel adapters are registered.
	// Without adapters (e.g. in tests), ReceiveMessage() reads the outbox directly.
	if len(p.channels) > 0 {
		p.startOutboxConsumer(runner)
	}

	log.Info("agent started", "provider", config.ModelConfig.Provider)

	// Register heartbeat if configured.
	if p.Lifecycle.Scheduler != nil && config.HeartbeatJSON != "" {
		var hbCfg types.HeartbeatConfig
		if json.Unmarshal([]byte(config.HeartbeatJSON), &hbCfg) == nil && hbCfg.Enabled {
			if err := p.Lifecycle.Scheduler.RegisterHeartbeat(ctx, config.ID, hbCfg, p.Lifecycle.Scheduler.DefaultTimezone()); err != nil {
				log.Warn("heartbeat registration failed", "error", err)
			}
		}
	}

	// Audit log the start event
	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   config.ID,
		EventType: types.EventAgentLifecycle,
		Action:    "start",
		Details:   fmt.Sprintf("agent %s started with provider %s", config.ID, config.ModelConfig.Provider),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

func hasHostAccessConfig(config types.AgentConfig) bool {
	if config.HostPaths != nil {
		if len(config.HostPaths.Read) > 0 || len(config.HostPaths.Write) > 0 {
			return true
		}
	}
	if config.HostFilesystem != nil {
		if len(config.HostFilesystem.Allowlist) > 0 {
			return true
		}
	}
	return false
}

// runAgent is the goroutine loop for a single agent.
func (p *Kyvik) runAgent(ctx context.Context, runner *AgentRunner) {
	log := slog.With("agent_id", runner.config.ID)
	defer close(runner.done)

	log.Info("agent goroutine started")
	runner.setStatus(types.AgentStatusRunning)
	_ = p.store.SetActualState(context.Background(), runner.config.ID, types.AgentStatusRunning, "")

	if p.Storage.Queue != nil {
		p.runAgentQueued(ctx, runner, log)
	} else {
		p.runAgentInbox(ctx, runner, log)
	}
}

// runAgentQueued reads messages from the persistent queue.
func (p *Kyvik) runAgentQueued(ctx context.Context, runner *AgentRunner, log *slog.Logger) {
	ch := p.Storage.Queue.Dequeue(ctx, runner.config.ID)
	agentID := runner.config.ID

	for {
		select {
		case <-ctx.Done():
			// Check if this is a kill (desired_state already set) vs a normal stop.
			if cfg, err := p.store.GetAgent(context.Background(), agentID); err == nil && cfg.DesiredState == types.DesiredStateKilled {
				runner.setStatus(types.AgentStatusKilled)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusKilled, "")
				log.Info("agent goroutine killed")
			} else {
				runner.setStatus(types.AgentStatusStopped)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusStopped, "")
				log.Info("agent goroutine stopped")
			}
			return
		case qmsg, ok := <-ch:
			if !ok {
				runner.setStatus(types.AgentStatusStopped)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusStopped, "")
				log.Info("queue channel closed, agent goroutine exiting")
				return
			}

			log.Debug("message received from queue", "id", qmsg.ID, "content_len", len(qmsg.Content))

			if qmsg.Channel == "internal" && p.teamManager != nil {
				if team, err := p.teamManager.GetTeamForAgent(ctx, agentID); err == nil && team != nil && !team.Active {
					log.Info("team paused; dropping internal queue delivery", "agent_id", agentID, "team_id", team.ID, "message_id", qmsg.ID)
					continue
				}
			}

			if err := p.Storage.Queue.MarkProcessing(ctx, qmsg.ID); err != nil {
				log.Error("failed to mark message processing", "id", qmsg.ID, "error", err)
			}

			msg := types.Message{
				AgentID:        qmsg.AgentID,
				Channel:        qmsg.Channel,
				ConversationID: qmsg.ConversationID,
				Sender:         qmsg.Sender,
				Role:           "user",
				Content:        qmsg.Content,
				Timestamp:      timeutil.NowUTC(),
			}
			if qmsg.Attachments != "" {
				_ = json.Unmarshal([]byte(qmsg.Attachments), &msg.Attachments)
			}

			// Enrich inter-agent messages with sender metadata so the
			// receiving agent knows who sent the message and its type.
			if qmsg.Channel == "internal" && qmsg.Sender != "" {
				senderName := qmsg.Sender
				if cfg, err := p.store.GetAgent(ctx, qmsg.Sender); err == nil {
					senderName = cfg.Name
				}
				teamName := ""
				if p.teamManager != nil {
					if team, err := p.teamManager.GetTeamForAgent(ctx, qmsg.Sender); err == nil && team != nil {
						teamName = team.Name
					}
				}
				msgType := qmsg.MessageType
				if msgType == "" {
					msgType = "message"
				}
				header := fmt.Sprintf("[Message from agent %q", senderName)
				if teamName != "" {
					header += fmt.Sprintf(" (team: %s)", teamName)
				}
				header += fmt.Sprintf(", type: %s", msgType)
				header += "]\n\n"
				msg.Content = header + msg.Content
			}

			if err := p.handleMessage(ctx, runner, msg); err != nil {
				log.Error("message handling failed", "id", qmsg.ID, "error", err)

				// Circuit breaker trip: quarantine and stop processing.
				if errors.Is(err, types.ErrCircuitBreakerTripped) {
					runner.outbox <- types.Message{
						AgentID:   runner.config.ID,
						Role:      "assistant",
						Content:   fmt.Sprintf("Circuit breaker tripped: %v", err),
						Timestamp: timeutil.NowUTC(),
					}
					_ = p.QuarantineAgent(context.Background(), agentID)
					return
				}

				runner.setStatus(types.AgentStatusError)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusError, err.Error())
				if p.Communication.Notifier != nil {
					_ = p.Communication.Notifier.Send(context.Background(), notifications.Event{
						Type:      "agent_error",
						Severity:  "warning",
						Agent:     agentID,
						Title:     "Agent message handling failed",
						Detail:    err.Error(),
						Timestamp: timeutil.NowUTC(),
					})
				}
				runner.outbox <- types.Message{
					AgentID:   runner.config.ID,
					Role:      "assistant",
					Content:   fmt.Sprintf("error: %v", err),
					Timestamp: timeutil.NowUTC(),
				}
				if failErr := p.Storage.Queue.Fail(ctx, qmsg.ID); failErr != nil {
					log.Error("failed to mark message failed", "id", qmsg.ID, "error", failErr)
				}
				if replayErr := p.Storage.Queue.ReplayAgent(ctx, agentID); replayErr != nil {
					log.Warn("queue replay failed after message failure", "error", replayErr)
				}
				runner.setStatus(types.AgentStatusRunning)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusRunning, "")
			} else {
				if err := p.Storage.Queue.Complete(ctx, qmsg.ID); err != nil {
					log.Error("failed to mark message completed", "id", qmsg.ID, "error", err)
				}
				if replayErr := p.Storage.Queue.ReplayAgent(ctx, agentID); replayErr != nil {
					log.Warn("queue replay failed after message completion", "error", replayErr)
				}
			}
		}
	}
}

// runAgentInbox reads messages from the in-memory inbox (original path).
func (p *Kyvik) runAgentInbox(ctx context.Context, runner *AgentRunner, log *slog.Logger) {
	agentID := runner.config.ID
	for {
		select {
		case <-ctx.Done():
			// Check if this is a kill (desired_state already set) vs a normal stop.
			if cfg, err := p.store.GetAgent(context.Background(), agentID); err == nil && cfg.DesiredState == types.DesiredStateKilled {
				runner.setStatus(types.AgentStatusKilled)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusKilled, "")
				log.Info("agent goroutine killed")
			} else {
				runner.setStatus(types.AgentStatusStopped)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusStopped, "")
				log.Info("agent goroutine stopped")
			}
			return
		case msg := <-runner.inbox:
			log.Debug("message received from inbox", "role", msg.Role, "content_len", len(msg.Content))
			if err := p.handleMessage(ctx, runner, msg); err != nil {
				log.Error("message handling failed", "error", err)

				// Circuit breaker trip: quarantine and stop processing.
				if errors.Is(err, types.ErrCircuitBreakerTripped) {
					runner.outbox <- types.Message{
						AgentID:   runner.config.ID,
						Role:      "assistant",
						Content:   fmt.Sprintf("Circuit breaker tripped: %v", err),
						Timestamp: timeutil.NowUTC(),
					}
					_ = p.QuarantineAgent(context.Background(), agentID)
					return
				}

				runner.setStatus(types.AgentStatusError)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusError, err.Error())
				if p.Communication.Notifier != nil {
					_ = p.Communication.Notifier.Send(context.Background(), notifications.Event{
						Type:      "agent_error",
						Severity:  "warning",
						Agent:     agentID,
						Title:     "Agent message handling failed",
						Detail:    err.Error(),
						Timestamp: timeutil.NowUTC(),
					})
				}
				runner.outbox <- types.Message{
					AgentID:   runner.config.ID,
					Role:      "assistant",
					Content:   fmt.Sprintf("error: %v", err),
					Timestamp: timeutil.NowUTC(),
				}
				runner.setStatus(types.AgentStatusRunning)
				_ = p.store.SetActualState(context.Background(), agentID, types.AgentStatusRunning, "")
			}
		}
	}
}

// handleMessage processes a single inbound message for an agent.
func (p *Kyvik) handleMessage(ctx context.Context, runner *AgentRunner, msg types.Message) error {
	agentID := runner.config.ID
	log := slog.With("agent_id", agentID)

	// Check budget before calling the model
	log.Debug("checking budget")
	budget, err := p.spending.CheckBudget(ctx, agentID)
	if err != nil {
		return fmt.Errorf("checking budget: %w", err)
	}
	if !budget.WithinBudget {
		log.Error("budget exceeded")
		return types.ErrBudgetExceeded
	}

	// Assemble context with budget enforcement
	asm := p.assembler
	if asm == nil {
		asm = ctxbudget.New(p.Storage.Memory, p.Storage.History)
	}
	channel := msg.Channel
	if channel == "" {
		channel = "internal"
	}
	channelID := ""
	if msg.ConversationID != "" {
		channelID = msg.ConversationID
	} else if len(runner.config.Channels) > 0 {
		channelID = runner.config.Channels[0].ChannelID
	}
	ep := p.EmbeddingProvider(runner.config.ModelConfig.Provider)

	// Pre-estimate tool definition tokens so the assembler can reserve headroom.
	var toolTokenEstimate int
	if p.ktpRegistry != nil {
		agentTier := ktp.ResolveAgentTier(runner.config.Template)
		defs := p.ktpRegistry.GetToolDefinitionsForModel(agentID, agentTier, runner.config.ToolGrants)
		for _, d := range defs {
			// Each tool definition contributes: name + description + JSON schema.
			// Estimate ~50 tokens per tool action as a reasonable baseline.
			toolTokenEstimate += 50 + ctxbudget.EstimateTokens(d.Description)
			if d.Parameters != nil {
				paramBytes, _ := json.Marshal(d.Parameters)
				toolTokenEstimate += ctxbudget.EstimateTokens(string(paramBytes))
			}
		}
	}
	currentMsgEstimate := ctxbudget.EstimateTokens(msg.Content)

	assembled, assembleErr := asm.Assemble(ctx, runner.config, msg.Content, ctxbudget.AssembleOptions{
		EmbeddingProvider:  ep,
		Channel:            channel,
		ChannelID:          channelID,
		MessageMetadata:    msg.Metadata,
		MessageCount:       runner.messageCount,
		ToolTokenEstimate:  toolTokenEstimate,
		CurrentMsgEstimate: currentMsgEstimate,
	})
	if assembleErr != nil {
		return fmt.Errorf("assemble context: %w", assembleErr)
	}
	log.Debug("context assembled", "tokens", assembled.TokenEstimate)

	// Pre-emptive compression: if assembled tokens exceed context budget,
	// compress synchronously before the model call to avoid a guaranteed
	// context-overflow error from the provider.
	ctxBudget := types.NormalizeContextBudget(runner.config.ContextBudget)
	if p.Lifecycle.Compressor != nil && ctxBudget.MaxTotalTokens > 0 &&
		assembled.TokenEstimate > ctxBudget.MaxTotalTokens {
		var compCfg types.CompressionConfig
		if runner.config.CompressionJSON != "" {
			json.Unmarshal([]byte(runner.config.CompressionJSON), &compCfg)
		}
		compCfg = types.NormalizeCompressionConfig(compCfg)
		if compCfg.Enabled {
			log.Warn("context exceeds budget, compressing before model call",
				"tokens", assembled.TokenEstimate, "budget", ctxBudget.MaxTotalTokens)
			if err := p.Lifecycle.Compressor.TryCompress(ctx, agentID, channel, channelID,
				compCfg, runner.config); err != nil {
				log.Warn("pre-emptive compression failed", "error", err)
			} else {
				// Re-assemble after compression
				assembled, assembleErr = asm.Assemble(ctx, runner.config, msg.Content, ctxbudget.AssembleOptions{
					EmbeddingProvider:  ep,
					Channel:            channel,
					ChannelID:          channelID,
					MessageMetadata:    msg.Metadata,
					MessageCount:       runner.messageCount,
					ToolTokenEstimate:  toolTokenEstimate,
					CurrentMsgEstimate: currentMsgEstimate,
				})
				if assembleErr != nil {
					return fmt.Errorf("re-assemble after compression: %w", assembleErr)
				}
				log.Info("context re-assembled after compression", "tokens", assembled.TokenEstimate)
			}
		}
	}

	// Security: inject canary token into system prompt.
	var secCanary *security.CanaryToken
	if p.Security.Defense != nil {
		secCfg := security.ResolveConfig(runner.config)
		assembled.SystemPrompt, secCanary = p.Security.Defense.PrepareSystemPrompt(ctx, secCfg, agentID, assembled.SystemPrompt)
	}

	messages := []models.ChatMessage{{Role: "system", Content: assembled.SystemPrompt}}
	messages = append(messages, assembled.Messages...)
	content := msg.Content
	if runner.config.TimestampMessages && msg.Role == "user" {
		now := timeutil.NowUTC()
		ts := now.Format("2006-01-02 15:04 MST")
		if tz := msg.Metadata["timezone"]; tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				ts = now.In(loc).Format("2006-01-02 15:04 MST") + " / " + now.Format("15:04 MST")
			}
		}
		content = fmt.Sprintf("<msg-timestamp time=\"%s\" />\n%s", ts, content)
	}
	messages = append(messages, models.ChatMessage{
		Role:        msg.Role,
		Content:     content,
		Attachments: msg.Attachments,
	})

	// Resolve model slot for this request
	modelName := runner.config.ModelConfig.Model
	activeProvider := runner.provider
	routedBy := "default"
	activeSlotName := "default"

	if p.Communication.Router != nil {
		incoming := router.IncomingMessage{Content: msg.Content, Attachments: msg.Attachments}

		var recentHistory []history.HistoryEntry
		if p.Storage.History != nil {
			recentHistory, _ = p.Storage.History.Recent(ctx, agentID, channel, channelID, 3)
		}

		decision, routeErr := p.Communication.Router.Route(ctx, agentID, incoming, runner.config, recentHistory)
		if routeErr != nil {
			return fmt.Errorf("routing message: %w", routeErr)
		}

		modelName = decision.Slot.Model
		activeProvider = decision.Provider
		routedBy = decision.RoutedBy
		activeSlotName = decision.Slot.Name

		// Prefix strips the message content for the model prompt
		if decision.RoutedBy == "prefix" && decision.Message != msg.Content {
			messages[len(messages)-1] = models.ChatMessage{
				Role:        msg.Role,
				Content:     decision.Message,
				Attachments: msg.Attachments,
			}
		}

		// Record classifier spending if used
		if decision.ClassifierCost.Cost > 0 {
			classifierCostSource := ""
			if decision.ClassifierCost.Cost > 0 {
				classifierCostSource = spending.CostSourceProviderReported
			}
			_ = p.spending.Record(ctx, agentID,
				decision.ClassifierCost.TokensIn, decision.ClassifierCost.TokensOut,
				decision.ClassifierCost.Cost, spending.RecordOptions{
					Model:       decision.ClassifierCost.Model,
					ModelSlot:   decision.ClassifierCost.SlotName,
					RoutedBy:    "system:classifier",
					Provider:    decision.ClassifierCost.Provider,
					CostSource:  classifierCostSource,
					UsageSource: spending.UsageSourceProvider,
				})
		}

		// Audit non-default routing decisions
		if routedBy != "default" {
			_ = p.audit.Log(ctx, types.AuditEntry{
				AgentID:   agentID,
				EventType: types.EventModelRequest,
				Action:    "model_routed",
				Details:   decision.Details,
				Timestamp: timeutil.NowUTC(),
			})
		}

		log.Debug("message routed", "slot", activeSlotName, "routed_by", routedBy,
			"model", modelName, "details", decision.Details)
	}

	// Build tool definitions from KTP registry if available.
	var toolDefs []models.ToolDefinition
	if p.ktpRegistry != nil {
		agentTier := ktp.ResolveAgentTier(runner.config.Template)
		log.Debug("ktp: resolved agent tier", "template", runner.config.Template, "tier", agentTier)
		ktpDefs := p.ktpRegistry.GetToolDefinitionsForModel(agentID, agentTier, runner.config.ToolGrants)
		filteredDefs := make([]ktp.ModelToolDefinition, 0, len(ktpDefs))
		for _, def := range ktpDefs {
			toolName, _ := ktp.SplitToolAction(def.Name)
			visible, visErr := p.isTeamToolVisible(ctx, agentID, toolName)
			if visErr != nil {
				log.Warn("ktp: failed to evaluate team tool visibility", "tool", toolName, "error", visErr)
				continue
			}
			if visible {
				filteredDefs = append(filteredDefs, def)
			}
		}
		ktpDefs = filteredDefs
		log.Debug("ktp: tool definitions for model", "tool_count", len(ktpDefs), "grant_count", len(runner.config.ToolGrants))
		for _, d := range ktpDefs {
			toolDefs = append(toolDefs, models.ToolDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			})
		}
	}
	if len(toolDefs) == 0 {
		log.Warn("ktp: no tools available for agent", "template", runner.config.Template, "grants", runner.config.ToolGrants)
	}

	req := models.CompletionRequest{
		Model:          modelName,
		Messages:       messages,
		Tools:          toolDefs,
		ProviderIgnore: runner.config.ProviderIgnore,
	}

	// Attach agent ID to context for per-agent key resolution
	ctx = models.WithAgentID(ctx, agentID)

	// Save user message to history before the loop.
	if p.Storage.History != nil {
		_ = p.Storage.History.Append(ctx, history.HistoryEntry{
			AgentID:     agentID,
			Channel:     channel,
			ChannelID:   channelID,
			Role:        msg.Role,
			Content:     msg.Content,
			Sender:      "",
			Tokens:      history.EstimateTokens(msg.Content),
			Attachments: attachmentMeta(msg.Attachments),
		})
	}

	// Resolve streaming adapter for real-time webui delivery.
	var streamer channels.StreamingAdapter
	if msg.Channel == "webui" {
		for _, adapter := range p.channels {
			if s, ok := adapter.(channels.StreamingAdapter); ok {
				streamer = s
				break
			}
		}
	}
	var streamedFinal bool // true if Stream() already sent chunks to webui

	// Tool-use loop: call the model, execute any tool calls, feed results back.
	// Keep this high enough for multi-step autonomous workflows, but bounded to
	// avoid runaway loops when providers repeatedly emit tool calls.
	maxToolIterations := configuredMaxToolIterations()
	var finalResp *models.CompletionResponse
	var lastResp *models.CompletionResponse

	// Compute a hard token ceiling for messages sent to the model.
	// This prevents context overflow during the tool-use loop where messages accumulate.
	msgTokenCeiling := ctxBudget.MaxTotalTokens
	if msgTokenCeiling <= 0 {
		msgTokenCeiling = types.DefaultContextMaxTotalTokens
	}

	for iteration := 0; iteration < maxToolIterations; iteration++ {
		// Before each model call, estimate total message tokens and trim
		// oldest non-system messages if we exceed the budget.
		if iteration > 0 {
			var totalMsgTokens int
			for _, m := range req.Messages {
				totalMsgTokens += ctxbudget.EstimateTokens(m.Content)
			}
			totalMsgTokens += toolTokenEstimate // tool definitions are constant
			if totalMsgTokens > msgTokenCeiling {
				req.Messages = trimOldestToolMessages(req.Messages, totalMsgTokens-msgTokenCeiling)
				log.Warn("trimmed tool loop messages to fit context budget",
					"iteration", iteration, "ceiling", msgTokenCeiling)
			}
		}
		log.Debug("calling model provider", "model", modelName, "iteration", iteration)

		var resp *models.CompletionResponse
		var err error

		// For webui messages without tools, try real streaming (token-by-token).
		// This gives the best UX: the user sees tokens appear as the model generates
		// them. We only do this when there are no tools (so no tool calls possible)
		// and security is disabled (canary validation needs the full response).
		canStream := streamer != nil && len(req.Tools) == 0 && p.Security.Defense == nil
		if canStream {
			resp, err = p.streamModelCall(ctx, activeProvider, req, agentID, msg.ConversationID, streamer, log)
			if err != nil {
				log.Warn("streaming failed, falling back to Complete()", "error", err)
				resp, err = nil, nil // reset for Complete() fallback
			} else {
				streamedFinal = true
			}
		}

		if resp == nil {
			resp, err = activeProvider.Complete(ctx, req)
		}

		if err != nil {
			// If this is a 400 error related to tool calling, retry without tools.
			if isToolRelated400(err) && len(req.Tools) > 0 {
				log.Warn("tool-related 400 error, retrying without tools",
					"error", err)
				retryReq := req
				retryReq.Tools = nil
				retryReq.Messages = stripToolMessages(retryReq.Messages)
				resp, err = activeProvider.Complete(ctx, retryReq)
			}
			if err != nil {
				log.Error("model completion failed", "error", err)
				return fmt.Errorf("model completion: %w", err)
			}
		}

		// Strip DeepSeek internal markup that leaks when a provider doesn't
		// support structured tool calling for the routed model.
		if len(resp.ToolCalls) == 0 {
			resp.Content = stripDSMLMarkup(resp.Content)
		}
		log.Debug("model response received",
			"tokens_in", resp.TokensIn, "tokens_out", resp.TokensOut,
			"cost", resp.Cost, "stop_reason", resp.StopReason,
			"tool_calls", len(resp.ToolCalls))

		// Record spending for this iteration (non-fatal).
		costSource := ""
		if resp.Cost > 0 {
			costSource = spending.CostSourceProviderReported
		}
		_ = p.spending.Record(ctx, agentID, resp.TokensIn, resp.TokensOut, resp.Cost, spending.RecordOptions{
			Model:       modelName,
			ModelSlot:   activeSlotName,
			RoutedBy:    routedBy,
			Provider:    activeProvider.Name(),
			CostSource:  costSource,
			UsageSource: spending.UsageSourceProvider,
		})

		// Circuit breaker: check spending velocity.
		if p.Lifecycle.Breaker != nil && resp.Cost > 0 {
			if trip := p.Lifecycle.Breaker.RecordSpending(runner.config, resp.Cost); trip != nil {
				return fmt.Errorf("%w: %s", types.ErrCircuitBreakerTripped, trip.Description)
			}
		}

		lastResp = resp

		// Circuit breaker: check for response loop patterns.
		if p.Lifecycle.Breaker != nil && resp.Content != "" {
			if trip := p.Lifecycle.Breaker.RecordMessage(runner.config, resp.Content); trip != nil {
				return fmt.Errorf("%w: %s", types.ErrCircuitBreakerTripped, trip.Description)
			}
		}

		// If no tool calls or executor not configured — done.
		if len(resp.ToolCalls) == 0 || p.ktpExecutor == nil {
			finalResp = resp
			break
		}

		// Append assistant message with tool calls to conversation.
		assistantMsg := models.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		req.Messages = append(req.Messages, assistantMsg)

		// Save tool-call assistant message to history.
		if p.Storage.History != nil {
			toolCallJSON, _ := json.Marshal(resp.ToolCalls)
			_ = p.Storage.History.Append(ctx, history.HistoryEntry{
				AgentID:       agentID,
				Channel:       channel,
				ChannelID:     channelID,
				Role:          "assistant",
				Content:       resp.Content,
				Sender:        runner.config.Name,
				Tokens:        history.EstimateTokens(resp.Content),
				ToolCallsJSON: string(toolCallJSON),
			})
		}

		// Execute each tool call through the KTP pipeline.
		for _, tc := range resp.ToolCalls {
			ktpCall := ktp.ModelToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: toMapStringAny(tc.Parameters),
			}

			ktpReq := ktp.ConvertToKTPRequest(agentID, ktpCall)
			ktpReq.TeamID = runner.config.TeamID

			// If the action is empty (no separator in name), resolve to first action.
			if ktpReq.Action == "" {
				if p.ktpRegistry == nil {
					log.Error("KTP registry is nil, cannot resolve default action", "tool", ktpReq.Tool)
					result := ktp.ConvertToModelResult(tc.ID, &ktp.ToolResponse{
						RequestID: ktpReq.ID,
						Success:   false,
						Error:     fmt.Sprintf("tool registry unavailable, cannot resolve action for %q", ktpReq.Tool),
						Timestamp: timeutil.NowUTC(),
					})
					req.Messages = append(req.Messages, models.ChatMessage{
						Role:       "tool",
						Content:    result.Content,
						ToolCallID: tc.ID,
					})
					continue
				}
				if tool, ok := p.ktpRegistry.Get(ktpReq.Tool); ok {
					decl := tool.Declaration()
					if len(decl.Actions) > 0 {
						ktpReq.Action = decl.Actions[0].Name
					}
				}
			}

			// Security: validate tool call before execution.
			if p.Security.Defense != nil {
				secCfg := security.ResolveConfig(runner.config)
				if vr := p.Security.Defense.ValidateToolCall(ctx, secCfg, agentID, ktpReq); vr != nil && vr.Blocked {
					log.Warn("tool call blocked by security", "tool", ktpReq.Tool, "reason", vr.BlockReason)
					result := ktp.ConvertToModelResult(tc.ID, &ktp.ToolResponse{
						RequestID: ktpReq.ID,
						Success:   false,
						Error:     "blocked by security policy: " + vr.BlockReason,
						Timestamp: timeutil.NowUTC(),
					})
					req.Messages = append(req.Messages, models.ChatMessage{
						Role:       "tool",
						Content:    result.Content,
						ToolCallID: tc.ID,
					})
					continue
				}
			}

			ktpResp, execErr := p.ktpExecutor.Execute(ctx, ktpReq)
			if execErr != nil {
				log.Error("KTP execution infrastructure error", "tool", ktpReq.Tool, "error", execErr)
				ktpResp = &ktp.ToolResponse{
					RequestID: ktpReq.ID,
					Success:   false,
					Error:     execErr.Error(),
					Timestamp: timeutil.NowUTC(),
				}
			} else if ktpResp != nil && !ktpResp.Success {
				log.Warn("tool call failed", "tool", ktpReq.Tool, "action", ktpReq.Action, "error", ktpResp.Error)
			}

			// Circuit breaker: record tool call result.
			if p.Lifecycle.Breaker != nil {
				destructive := false
				if p.ktpRegistry != nil {
					if tool, ok := p.ktpRegistry.Get(ktpReq.Tool); ok {
						if actionSpec, found := tool.Declaration().GetAction(ktpReq.Action); found {
							destructive = actionSpec.Destructive
						}
					}
				}
				if trip := p.Lifecycle.Breaker.RecordToolCall(runner.config, ktpReq.Tool, ktpReq.Action, ktpResp.Success, destructive); trip != nil {
					return fmt.Errorf("%w: %s", types.ErrCircuitBreakerTripped, trip.Description)
				}
			}

			result := ktp.ConvertToModelResult(tc.ID, ktpResp)

			// Security: sanitize, wrap boundaries, and reinforce tool result.
			if p.Security.Defense != nil {
				secCfg := security.ResolveConfig(runner.config)
				result = p.Security.Defense.ProcessToolResult(ctx, secCfg, runner.config.ID, runner.config.Name, tc.Name, result)
			}

			// Run feedback hooks on successful tool calls.
			if p.feedbackRunner != nil && ktpResp.Success {
				var hooksCfg types.FeedbackHooksConfig
				if runner.config.FeedbackHooksJSON != "" {
					json.Unmarshal([]byte(runner.config.FeedbackHooksJSON), &hooksCfg)
				}
				if hooksCfg.Enabled {
					workspace := p.workspaceRoot + "/" + agentID
					fb := p.feedbackRunner.RunHooks(ctx, hooksCfg,
						ktpReq.Tool, ktpReq.Action, ktpReq.Parameters, workspace, true)
					if fb != "" {
						result.Content += fb
					}
				}
			}

			// Append tool result to conversation.
			req.Messages = append(req.Messages, models.ChatMessage{
				Role:       "tool",
				Content:    result.Content,
				ToolCallID: tc.ID,
			})

			// Save tool result to history.
			if p.Storage.History != nil {
				_ = p.Storage.History.Append(ctx, history.HistoryEntry{
					AgentID:    agentID,
					Channel:    channel,
					ChannelID:  channelID,
					Role:       "tool",
					Content:    result.Content,
					Tokens:     history.EstimateTokens(result.Content),
					ToolCallID: tc.ID,
				})
			}
		}

		// Check budget before next iteration.
		budget, budgetErr := p.spending.CheckBudget(ctx, agentID)
		if budgetErr != nil || !budget.WithinBudget {
			hasToolResults := len(resp.ToolCalls) > 0
			if hasToolResults {
				toolNames := make([]string, len(resp.ToolCalls))
				for i, tc := range resp.ToolCalls {
					toolNames[i] = tc.Name
				}
				log.Warn("budget exceeded with unprocessed tool results",
					"iteration", iteration,
					"tools", toolNames,
					"within_budget", budget.WithinBudget,
					"budget_error", budgetErr,
				)
				resp.UnprocessedToolResults = true
				resp.Content += "\n\n[Budget exceeded: tool results from " +
					fmt.Sprintf("%v", toolNames) +
					" were obtained but not incorporated into the response.]"
			} else {
				log.Warn("budget exceeded during tool-use loop", "iteration", iteration)
			}
			finalResp = resp
			break
		}
	}

	// Fallback: if the loop exhausted maxToolIterations without a break,
	// finalResp is still nil. Use the last response received from the model.
	if finalResp == nil {
		if lastResp != nil {
			log.Warn("tool-use loop hit max iterations without final response",
				"max_iterations", maxToolIterations)
			finalResp = lastResp
			if len(finalResp.ToolCalls) > 0 {
				limitNote := fmt.Sprintf("\n\n[Tool loop paused after %d iterations to prevent runaway execution. Re-send your last message or ask to continue to finish remaining steps.]", maxToolIterations)
				if strings.TrimSpace(finalResp.Content) == "" {
					finalResp.Content = "I reached the tool-iteration safety limit before producing a final response." + limitNote
				} else {
					finalResp.Content += limitNote
				}
			}
		} else {
			return fmt.Errorf("tool-use loop produced no model response")
		}
	}

	// Save final assistant response to history.
	if p.Storage.History != nil {
		_ = p.Storage.History.Append(ctx, history.HistoryEntry{
			AgentID:   agentID,
			Channel:   channel,
			ChannelID: channelID,
			Role:      "assistant",
			Content:   finalResp.Content,
			Sender:    runner.config.Name,
			Tokens:    history.EstimateTokens(finalResp.Content),
		})

		// Auto-trim: only when compression is not enabled (compression manages its own history).
		var compCfg types.CompressionConfig
		if runner.config.CompressionJSON != "" {
			json.Unmarshal([]byte(runner.config.CompressionJSON), &compCfg)
		}
		if !compCfg.Enabled {
			limit := runner.config.HistoryLimit
			if limit <= 0 {
				limit = history.DefaultLimit
			}
			if count, err := p.Storage.History.Count(ctx, agentID, channel, channelID); err == nil && count > limit*2 {
				if trimmed, err := p.Storage.History.Trim(ctx, agentID, channel, channelID, limit); err != nil {
					log.Warn("history trim failed", "error", err)
				} else if trimmed > 0 {
					log.Debug("trimmed conversation history", "deleted", trimmed)
				}
			}
		}
	}

	// Trigger automatic memory extraction every Interval messages
	if runner.config.AutoExtractMemories && p.Storage.Memory != nil && p.Storage.History != nil {
		extractCfg := memory.ExtractionConfig{
			Interval:           runner.config.MemoryExtractionInterval,
			MaxPerRun:          runner.config.MemoryMaxExtractionsPerRun,
			DuplicateThreshold: runner.config.MemoryDuplicateThreshold,
			SimilarThreshold:   runner.config.MemorySimilarThreshold,
		}
		if extractCfg.Interval == 0 {
			extractCfg = memory.DefaultExtractionConfig()
		}

		runner.messageCount++
		if runner.messageCount%extractCfg.Interval == 0 {
			go func() {
				extractCtx := context.Background()
				extractCtx = models.WithAgentID(extractCtx, agentID)

				// Check budget before extraction
				budget, budgetErr := p.spending.CheckBudget(extractCtx, agentID)
				if budgetErr != nil || !budget.WithinBudget {
					slog.Debug("skipping memory extraction, budget exceeded", "agent_id", agentID)
					return
				}

				// Load recent history
				channel := msg.Channel
				if channel == "" {
					channel = "internal"
				}
				channelID := ""
				if msg.ConversationID != "" {
					channelID = msg.ConversationID
				} else if len(runner.config.Channels) > 0 {
					channelID = runner.config.Channels[0].ChannelID
				}
				recent, recentErr := p.Storage.History.Recent(extractCtx, agentID, channel, channelID, 10)
				if recentErr != nil {
					return
				}

				ep := p.EmbeddingProvider(runner.config.ModelConfig.Provider)
				extractor := memory.NewExtractor(p.Storage.Memory, runner.provider, ep, runner.config.ModelConfig.Model, extractCfg)
				extractor.Extract(extractCtx, agentID, recent)
			}()
		}
	}

	// Background compression trigger.
	if p.Lifecycle.Compressor != nil {
		var compCfg types.CompressionConfig
		if runner.config.CompressionJSON != "" {
			json.Unmarshal([]byte(runner.config.CompressionJSON), &compCfg)
		}
		compCfg = types.NormalizeCompressionConfig(compCfg)
		if compCfg.Enabled {
			go p.Lifecycle.Compressor.TryCompress(context.Background(), agentID, channel, channelID,
				compCfg, runner.config)
		}
	}

	// Security: validate response for canary leaks.
	responseContent := finalResp.Content
	if p.Security.Defense != nil {
		secCfg := security.ResolveConfig(runner.config)
		responseContent = p.Security.Defense.ValidateResponse(ctx, secCfg, agentID, secCanary, responseContent, assembled.SystemPrompt)
	}

	// Stream response to webui subscribers (if applicable).
	// If Stream() was already used, the chunks were already delivered. If security
	// validation modified the content, we need to re-deliver the corrected version.
	if streamer != nil && msg.ConversationID != "" {
		if streamedFinal && responseContent == finalResp.Content {
			// Already streamed via Stream() and security didn't change anything — no-op.
			log.Debug("response already streamed to webui")
		} else {
			// Either we used Complete() (most common) or security modified the streamed
			// content. Deliver progressively as chunks.
			if streamedFinal {
				log.Warn("security modified streamed response, re-delivering corrected content")
			}
			deliverProgressiveChunks(streamer, agentID, msg.ConversationID, responseContent, finalResp)
			log.Debug("response delivered progressively to webui")
		}
	}

	// Send response to outbox (for all channel adapters including non-webui).
	runner.outbox <- types.Message{
		AgentID:        agentID,
		Channel:        msg.Channel,
		Sender:         msg.Sender,
		Role:           "assistant",
		Content:        responseContent,
		ConversationID: msg.ConversationID,
		Timestamp:      timeutil.NowUTC(),
	}
	log.Debug("response sent to outbox")

	return nil
}

func (p *Kyvik) isTeamToolVisible(ctx context.Context, agentID, toolName string) (bool, error) {
	switch toolName {
	case "team.delegate", "team.broadcast", "team.status", "team.recall":
	default:
		return true, nil
	}

	if p.teamManager == nil {
		return false, nil
	}
	team, err := p.teamManager.GetTeamForAgent(ctx, agentID)
	if err != nil {
		return false, err
	}
	if team == nil {
		return false, nil
	}

	switch toolName {
	case "team.delegate", "team.recall":
		return team.LeaderID == agentID, nil
	case "team.broadcast", "team.status":
		return true, nil
	default:
		return true, nil
	}
}

// streamModelCall calls provider.Stream() and forwards chunks to the webui
// streaming adapter in real time. Returns a CompletionResponse with the
// accumulated content. On error, the caller should fall back to Complete().
func (p *Kyvik) streamModelCall(
	ctx context.Context,
	provider models.Provider,
	req models.CompletionRequest,
	agentID, conversationID string,
	streamer channels.StreamingAdapter,
	log *slog.Logger,
) (*models.CompletionResponse, error) {
	streamCh, err := provider.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("stream request: %w", err)
	}

	var content strings.Builder
	streamTimeout := time.NewTimer(5 * time.Minute)
	defer streamTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-streamTimeout.C:
			_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
				Type:           "error",
				Error:          "response timed out",
				ConversationID: conversationID,
			})
			return nil, fmt.Errorf("stream timeout after 5 minutes")
		case chunk, ok := <-streamCh:
			if !ok {
				// Channel closed without Done — treat accumulated content as complete.
				goto done
			}

			if chunk.Error != "" {
				_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
					Type:           "error",
					Error:          chunk.Error,
					ConversationID: conversationID,
				})
				return nil, fmt.Errorf("stream error: %s", chunk.Error)
			}

			if chunk.Content != "" {
				content.WriteString(chunk.Content)
				_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
					Type:           "chunk",
					Content:        chunk.Content,
					ConversationID: conversationID,
				})
			}

			if chunk.Done {
				goto done
			}
		}
	}

done:
	accumulated := content.String()

	// Estimate tokens (providers may provide real counts in future StreamChunk versions).
	estimatedOut := int64(len(accumulated) / 4)

	_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
		Type:           "done",
		ConversationID: conversationID,
		Timestamp:      timeutil.NowUTC(),
		TokensOut:      estimatedOut,
	})

	log.Debug("stream completed", "content_len", len(accumulated), "estimated_tokens_out", estimatedOut)

	return &models.CompletionResponse{
		Content:   accumulated,
		TokensOut: estimatedOut,
	}, nil
}

// deliverProgressiveChunks sends a Complete() response to the webui as a
// series of chunk events followed by a done event. This gives progressive
// rendering even though the response was obtained in full from Complete().
func deliverProgressiveChunks(
	streamer channels.StreamingAdapter,
	agentID, conversationID, content string,
	resp *models.CompletionResponse,
) {
	for _, chunk := range splitIntoChunks(content, 40) {
		_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
			Type:           "chunk",
			Content:        chunk,
			ConversationID: conversationID,
		})
	}
	_ = streamer.SendStreamEvent(agentID, channels.StreamEvent{
		Type:           "done",
		ConversationID: conversationID,
		Timestamp:      timeutil.NowUTC(),
		TokensIn:       resp.TokensIn,
		TokensOut:      resp.TokensOut,
		Cost:           resp.Cost,
	})
}

// splitIntoChunks splits text into roughly equal-sized pieces, breaking on
// word boundaries where possible. targetSize is the approximate character
// count per chunk.
func splitIntoChunks(text string, targetSize int) []string {
	if targetSize <= 0 {
		targetSize = 40
	}
	if len(text) <= targetSize {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= targetSize {
			chunks = append(chunks, text)
			break
		}

		end := targetSize
		if end > len(text) {
			end = len(text)
		}

		// Search backward for a word boundary (space or newline).
		breakAt := -1
		for i := end; i > end/2; i-- {
			if text[i] == ' ' || text[i] == '\n' {
				breakAt = i + 1
				break
			}
		}
		if breakAt < 0 {
			breakAt = end
		}

		chunks = append(chunks, text[:breakAt])
		text = text[breakAt:]
	}

	return chunks
}

func configuredMaxToolIterations() int {
	v := strings.TrimSpace(os.Getenv("KYVIK_MAX_TOOL_ITERATIONS"))
	if v == "" {
		return defaultMaxToolIterations
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultMaxToolIterations
	}
	if n < minMaxToolIterations {
		return minMaxToolIterations
	}
	if n > hardMaxToolIterations {
		return hardMaxToolIterations
	}
	return n
}

// toMapStringAny converts a ToolUse.Parameters (interface{}) to map[string]any.
// If already map[string]any, returns directly. Otherwise roundtrips through JSON.
func toMapStringAny(v interface{}) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// Roundtrip through JSON for other types (e.g. map[string]interface{}).
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// StopAgent gracefully stops an agent runner and sets desired state to stopped.
func (p *Kyvik) StopAgent(ctx context.Context, agentID string) error {
	return p.stopAgentInternal(ctx, agentID, false)
}

// StartLocalAgent retrieves the agent config from the store and starts it locally.
// This is called via cluster callbacks when an agent is assigned to this node.
func (p *Kyvik) StartLocalAgent(ctx context.Context, agentID string) error {
	agent, err := p.store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent config: %w", err)
	}
	return p.StartAgent(ctx, *agent)
}

// stopAgentInternal stops an agent runner. When preserveDesiredState is true,
// the desired state is not changed (used during shutdown to preserve restart intent).
func (p *Kyvik) stopAgentInternal(ctx context.Context, agentID string, preserveDesiredState bool) error {
	log := slog.With("agent_id", agentID)

	p.mu.Lock()
	runner, ok := p.agents[agentID]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("%w: %s", types.ErrAgentNotRunning, agentID)
	}
	// Remove immediately to prevent double-stop races
	delete(p.agents, agentID)
	p.mu.Unlock()

	// Stop the outbox consumer first
	if runner.outboxCancel != nil {
		runner.outboxCancel()
		<-runner.outboxDone
	}

	// Signal the agent goroutine to stop
	runner.cancel()

	// Wait for goroutine to finish, respecting the caller's context deadline
	select {
	case <-runner.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Clean up circuit breaker state.
	if p.Lifecycle.Breaker != nil {
		p.Lifecycle.Breaker.Remove(agentID)
	}

	// Disable heartbeat schedule (not delete — will re-enable on resume).
	if p.Lifecycle.Scheduler != nil {
		_ = p.Lifecycle.Scheduler.UnregisterHeartbeat(ctx, agentID)
	}

	// Deprovision channel adapters
	p.deprovisionChannels(ctx, agentID, log)

	// Persist state changes
	if !preserveDesiredState {
		_ = p.store.SetDesiredState(ctx, agentID, types.DesiredStateStopped)
	}
	_ = p.store.SetActualState(ctx, agentID, types.AgentStatusStopped, "")

	log.Info("agent stopped")

	// Audit log the stop event
	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    "stop",
		Details:   fmt.Sprintf("agent %s stopped", agentID),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// GetAgentStatus returns the current status of an agent.
// Falls back to the store when the agent is not in memory.
func (p *Kyvik) GetAgentStatus(ctx context.Context, agentID string) (types.AgentStatus, error) {
	p.mu.RLock()
	runner, ok := p.agents[agentID]
	p.mu.RUnlock()
	if !ok {
		config, err := p.store.GetAgent(ctx, agentID)
		if err != nil {
			return types.AgentStatusStopped, nil
		}
		return config.ActualState, nil
	}
	return runner.getStatus(), nil
}

// SendMessage sends a message to an agent's inbox. When a persistent queue is
// configured, the message is enqueued to the database instead of pushed directly.
// Quarantined agents accept messages into the queue but do not process them.
func (p *Kyvik) SendMessage(ctx context.Context, agentID string, msg types.Message) error {
	p.mu.RLock()
	_, ok := p.agents[agentID]
	p.mu.RUnlock()

	if !ok && p.Storage.Queue != nil {
		// Agent not in memory — check if quarantined (accept messages into queue)
		config, err := p.store.GetAgent(ctx, agentID)
		if err == nil && config.DesiredState == types.DesiredStateQuarantined {
			_, err := p.Storage.Queue.Enqueue(ctx, queue.QueueMessage{
				AgentID:        agentID,
				Channel:        msg.Channel,
				ConversationID: msg.ConversationID,
				Content:        msg.Content,
				Attachments:    marshalAttachments(msg.Attachments),
			})
			return err
		}
		return fmt.Errorf("%w: %s", types.ErrAgentNotRunning, agentID)
	}
	if !ok {
		return fmt.Errorf("%w: %s", types.ErrAgentNotRunning, agentID)
	}

	if p.Storage.Queue != nil {
		_, err := p.Storage.Queue.Enqueue(ctx, queue.QueueMessage{
			AgentID:        agentID,
			Channel:        msg.Channel,
			ConversationID: msg.ConversationID,
			Content:        msg.Content,
			Attachments:    marshalAttachments(msg.Attachments),
		})
		return err
	}

	// Original inbox path (used when no queue is configured).
	p.mu.RLock()
	runner, ok := p.agents[agentID]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", types.ErrAgentNotRunning, agentID)
	}

	select {
	case runner.inbox <- msg:
		slog.Debug("message delivered to inbox", "agent_id", agentID, "role", msg.Role)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReceiveMessage receives a message from an agent's outbox.
func (p *Kyvik) ReceiveMessage(ctx context.Context, agentID string) (types.Message, error) {
	p.mu.RLock()
	runner, ok := p.agents[agentID]
	p.mu.RUnlock()
	if !ok {
		return types.Message{}, fmt.Errorf("%w: %s", types.ErrAgentNotRunning, agentID)
	}

	select {
	case msg := <-runner.outbox:
		return msg, nil
	case <-ctx.Done():
		return types.Message{}, ctx.Err()
	}
}

// ListAgents returns all configured agents and their statuses.
func (p *Kyvik) ListAgents(ctx context.Context) ([]types.AgentConfig, error) {
	return p.store.ListAgents(ctx)
}

// ProviderInfo describes a registered model provider.
type ProviderInfo struct {
	Name string
}

// ListProviders returns the names of all registered model providers,
// deduplicating aliases that point to the same adapter instance.
// When the same adapter is registered under both an instance ID and a
// bare provider-type name, the shorter (type) name is preferred.
func (p *Kyvik) ListProviders() []ProviderInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	// Map adapter identity → preferred name (shortest wins, so bare type
	// names like "openrouter" are chosen over ULID instance IDs).
	best := make(map[any]string, len(p.models))
	for name, adapter := range p.models {
		if prev, ok := best[adapter]; !ok || len(name) < len(prev) {
			best[adapter] = name
		}
	}
	out := make([]ProviderInfo, 0, len(best))
	for _, name := range best {
		out = append(out, ProviderInfo{Name: name})
	}
	return out
}

// SlackStatusProvider is the interface a Slack manager must implement
// for the core to query connection stats.
type SlackStatusProvider interface {
	PrimaryConnected() bool
	DedicatedCount() int
}

// SlackStatus returns the primary connection status and dedicated adapter count.
// Returns (false, 0) if the slack adapter doesn't support status queries.
func (p *Kyvik) SlackStatus() (primaryConnected bool, dedicatedCount int) {
	p.mu.RLock()
	adapter, ok := p.channels["slack"]
	p.mu.RUnlock()
	if !ok {
		return false, 0
	}
	if sp, ok := adapter.(SlackStatusProvider); ok {
		return sp.PrimaryConnected(), sp.DedicatedCount()
	}
	return false, 0
}

// ListChannelNames returns the names of all registered channel adapters.
func (p *Kyvik) ListChannelNames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.channels))
	for name := range p.channels {
		out = append(out, name)
	}
	return out
}

// ListProviderModels returns available models for a specific provider.
func (p *Kyvik) ListProviderModels(ctx context.Context, providerName string) ([]models.ModelInfo, error) {
	p.mu.RLock()
	provider, ok := p.models[providerName]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", types.ErrProviderUnavailable, providerName)
	}
	return provider.ListModels(ctx)
}

// GetAgent returns a single agent's config by ID.
func (p *Kyvik) GetAgent(ctx context.Context, agentID string) (*types.AgentConfig, error) {
	return p.store.GetAgent(ctx, agentID)
}

// CreateAgent persists a new agent configuration.
func (p *Kyvik) CreateAgent(ctx context.Context, config types.AgentConfig) error {
	return p.store.CreateAgent(ctx, config)
}

// UpdateAgent updates an agent's configuration. If the agent is running, it is stopped first.
func (p *Kyvik) UpdateAgent(ctx context.Context, config types.AgentConfig) error {
	// Stop the agent if it's currently running
	p.mu.RLock()
	_, running := p.agents[config.ID]
	p.mu.RUnlock()
	if running {
		if err := p.StopAgent(ctx, config.ID); err != nil {
			return fmt.Errorf("stopping agent before update: %w", err)
		}
	}

	if err := p.store.UpdateAgent(ctx, config); err != nil {
		return fmt.Errorf("updating agent: %w", err)
	}

	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   config.ID,
		EventType: types.EventAgentLifecycle,
		Action:    "update",
		Details:   fmt.Sprintf("agent %s configuration updated", config.ID),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// DeleteAgent removes an agent. If the agent is running, it is stopped first.
func (p *Kyvik) DeleteAgent(ctx context.Context, agentID string) error {
	// Stop the agent if it's currently running
	p.mu.RLock()
	_, running := p.agents[agentID]
	p.mu.RUnlock()
	if running {
		if err := p.StopAgent(ctx, agentID); err != nil {
			return fmt.Errorf("stopping agent before delete: %w", err)
		}
	}

	// Revoke per-agent API key if provisioned
	if p.keyProvisioner != nil {
		if err := p.keyProvisioner.RevokeKey(ctx, agentID); err != nil {
			slog.Warn("per-agent key revocation failed", "agent_id", agentID, "error", err)
		}
	}

	if err := p.store.DeleteAgent(ctx, agentID); err != nil {
		return fmt.Errorf("deleting agent: %w", err)
	}

	// Clean up all schedules (tasks + heartbeats) for the agent.
	if p.Lifecycle.Scheduler != nil {
		_ = p.Lifecycle.Scheduler.RemoveAgentSchedules(ctx, agentID)
	}

	// Clean up all skill grants for the agent.
	if p.skillManager != nil {
		_ = p.skillManager.CleanupAgent(ctx, agentID)
	}

	// Clean up conversation history and memories.
	if p.Storage.History != nil {
		_ = p.Storage.History.Clear(ctx, agentID)
	}
	if p.Storage.Conversations != nil {
		_ = p.Storage.Conversations.DeleteByAgent(ctx, agentID)
	}
	if p.Storage.Memory != nil {
		_ = p.Storage.Memory.DeleteByAgent(ctx, agentID)
	}

	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    "delete",
		Details:   fmt.Sprintf("agent %s deleted", agentID),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// ListTemplates returns all available permission templates.
func (p *Kyvik) ListTemplates(ctx context.Context) ([]permissions.Template, error) {
	return p.gate.ListTemplates(ctx)
}

// GetAgentCapabilities returns the effective permissions for an agent.
func (p *Kyvik) GetAgentCapabilities(ctx context.Context, agentID string) ([]types.Capability, error) {
	return p.gate.GetAgentCapabilities(ctx, agentID)
}

// ListOverrides returns all permission overrides for an agent.
func (p *Kyvik) ListOverrides(ctx context.Context, agentID string) ([]permissions.Override, error) {
	return p.gate.ListOverrides(ctx, agentID)
}

// AddOverride adds a granular permission override for an agent.
func (p *Kyvik) AddOverride(ctx context.Context, override permissions.Override) error {
	return p.gate.AddOverride(ctx, override)
}

// RemoveOverride removes a specific permission override.
func (p *Kyvik) RemoveOverride(ctx context.Context, agentID string, cap types.Capability) error {
	return p.gate.RemoveOverride(ctx, agentID, cap)
}

// RemoveAllOverrides removes all permission overrides for an agent.
func (p *Kyvik) RemoveAllOverrides(ctx context.Context, agentID string) error {
	return p.gate.RemoveAllOverrides(ctx, agentID)
}

// ResumeAgent starts a previously created (stopped) agent without re-persisting to store.
func (p *Kyvik) ResumeAgent(ctx context.Context, agentID string) error {
	log := slog.With("agent_id", agentID)

	if p.emergencyStop.Load() {
		return fmt.Errorf("emergency stop active: new agent starts are blocked until cleared")
	}

	if p.vacationMode.Load() {
		return fmt.Errorf("%w", types.ErrVacationModeActive)
	}

	// Load config from store
	config, err := p.store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("loading agent config: %w", err)
	}

	// Resolve model slots (backward-compatible: empty ModelSlotsJSON falls back to ModelConfig)
	resolved, err := router.ResolveSlots(*config)
	if err != nil {
		return fmt.Errorf("resolving model slots: %w", err)
	}
	if resolved.DefaultSlot.Provider == "" {
		return fmt.Errorf("model provider is required")
	}

	// Lookup provider for the default slot
	provider, ok := p.models[resolved.DefaultSlot.Provider]
	if !ok {
		return fmt.Errorf("%w: %s", types.ErrProviderUnavailable, resolved.DefaultSlot.Provider)
	}

	// Backfill ModelConfig for backward-compatible code paths (embedding, audit, etc.)
	if config.ModelSlotsJSON != "" {
		config.ModelConfig.Provider = resolved.DefaultSlot.Provider
		config.ModelConfig.Model = resolved.DefaultSlot.Model
	}

	// Update desired/actual state
	_ = p.store.SetDesiredState(ctx, agentID, types.DesiredStateRunning)
	_ = p.store.SetActualState(ctx, agentID, types.AgentStatusStarting, "")

	// Check agent not already running
	p.mu.Lock()
	if _, exists := p.agents[agentID]; exists {
		p.mu.Unlock()
		return fmt.Errorf("%w: %s", types.ErrAgentAlreadyRunning, agentID)
	}

	// Create the runner
	runner := &AgentRunner{
		config:   *config,
		provider: provider,
		status:   types.AgentStatusStarting,
		inbox:    make(chan types.Message, 16),
		outbox:   make(chan types.Message, 16),
		done:     make(chan struct{}),
	}

	p.agents[agentID] = runner
	p.mu.Unlock()

	// Initialize queue channel and replay pending messages for this agent.
	if p.Storage.Queue != nil {
		p.Storage.Queue.Dequeue(ctx, agentID)
		if err := p.Storage.Queue.ReplayAgent(ctx, agentID); err != nil {
			log.Warn("queue replay failed for agent", "error", err)
		}

		// Replay bus messages sent while the agent was offline.
		if internalAdapter, ok := p.channels["internal"]; ok {
			if ba, ok := internalAdapter.(*busadapter.Adapter); ok {
				if err := ba.ReplayUndelivered(ctx, agentID, p.Storage.Queue); err != nil {
					log.Warn("bus message replay failed", "error", err)
				}
			}
		}
	}

	// Create independent context
	agentCtx, cancel := context.WithCancel(context.Background())
	runner.cancel = cancel

	// Launch agent goroutine
	go p.runAgent(agentCtx, runner)

	// Provision channel adapters
	p.provisionChannels(ctx, *config, log)

	// Start outbox consumer only when channel adapters are registered.
	if len(p.channels) > 0 {
		p.startOutboxConsumer(runner)
	}

	// Re-enable heartbeat if configured.
	if p.Lifecycle.Scheduler != nil && config.HeartbeatJSON != "" {
		var hbCfg types.HeartbeatConfig
		if json.Unmarshal([]byte(config.HeartbeatJSON), &hbCfg) == nil && hbCfg.Enabled {
			_ = p.Lifecycle.Scheduler.EnableHeartbeat(ctx, agentID)
		}
	}

	log.Info("agent resumed", "provider", config.ModelConfig.Provider)

	// Audit log the resume event
	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    "resume",
		Details:   fmt.Sprintf("agent %s resumed with provider %s", agentID, config.ModelConfig.Provider),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// QuarantineAgent moves an agent to quarantine state. The agent goroutine is
// stopped, but messages continue to be accepted into the persistent queue.
func (p *Kyvik) QuarantineAgent(ctx context.Context, agentID string) error {
	log := slog.With("agent_id", agentID)

	_ = p.store.SetDesiredState(ctx, agentID, types.DesiredStateQuarantined)

	// Stop the agent if it's running (preserve desired=quarantined)
	p.mu.RLock()
	_, running := p.agents[agentID]
	p.mu.RUnlock()
	if running {
		if err := p.stopAgentInternal(ctx, agentID, true); err != nil {
			log.Warn("failed to stop agent during quarantine", "error", err)
		}
	}

	_ = p.store.SetActualState(ctx, agentID, types.AgentStatusQuarantined, "")

	log.Info("agent quarantined")
	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    "quarantine",
		Details:   fmt.Sprintf("agent %s quarantined", agentID),
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// Reconcile checks all persisted agents and starts those whose desired state
// is "running" but are not currently active. Called at startup to recover
// agents that were running before a crash or restart.
func (p *Kyvik) Reconcile(ctx context.Context) error {
	// Restore vacation mode from persisted state before reconciling agents.
	if err := p.LoadVacationState(ctx); err != nil {
		slog.Warn("reconcile: failed to load vacation state", "error", err)
	}

	agents, err := p.store.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("listing agents for reconciliation: %w", err)
	}

	// Reset stale actual states: anything that claims to be running/starting
	// must be stopped since the process just started (unless killed).
	for _, a := range agents {
		if a.ActualState == types.AgentStatusRunning || a.ActualState == types.AgentStatusStarting {
			if a.DesiredState == types.DesiredStateKilled {
				_ = p.store.SetActualState(ctx, a.ID, types.AgentStatusKilled, "")
			} else {
				_ = p.store.SetActualState(ctx, a.ID, types.AgentStatusStopped, "")
			}
		}
	}

	for _, a := range agents {
		switch a.DesiredState {
		case types.DesiredStateRunning:
			if p.vacationMode.Load() {
				slog.Info("reconcile: skipping agent start during vacation mode", "agent_id", a.ID)
				continue
			}
			p.mu.RLock()
			_, running := p.agents[a.ID]
			p.mu.RUnlock()
			if !running {
				slog.Info("reconcile: starting agent", "agent_id", a.ID)
				if err := p.reconcileStart(ctx, a.ID); err != nil {
					slog.Error("reconcile: failed to start agent", "agent_id", a.ID, "error", err)
				}
			}
		case types.DesiredStateQuarantined:
			_ = p.store.SetActualState(ctx, a.ID, types.AgentStatusQuarantined, "")
		case types.DesiredStateKilled:
			_ = p.store.SetActualState(ctx, a.ID, types.AgentStatusKilled, "")
			// DesiredStateStopped: no-op
		}
	}

	return nil
}

// reconcileStart attempts to resume an agent with exponential backoff.
func (p *Kyvik) reconcileStart(ctx context.Context, agentID string) error {
	const maxAttempts = 5
	var lastErr error
	delay := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := p.ResumeAgent(ctx, agentID); err != nil {
			lastErr = err
			slog.Warn("reconcile: start attempt failed",
				"agent_id", agentID,
				"attempt", attempt,
				"error", err,
			)
			if attempt < maxAttempts {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
		} else {
			return nil
		}
	}

	// All attempts exhausted
	_ = p.store.SetActualState(ctx, agentID, types.AgentStatusError, lastErr.Error())
	if p.Communication.Notifier != nil {
		_ = p.Communication.Notifier.Send(ctx, notifications.Event{
			Type:      "agent_error",
			Severity:  "critical",
			Agent:     agentID,
			Title:     "Agent reconciliation failed permanently",
			Detail:    fmt.Sprintf("Failed to start after %d attempts: %v", maxAttempts, lastErr),
			Timestamp: timeutil.NowUTC(),
		})
	}
	return fmt.Errorf("reconcile agent %s after %d attempts: %w", agentID, maxAttempts, lastErr)
}

// provisionChannels calls ProvisionAgent on each registered channel adapter
// that matches the agent's channel configuration.
func (p *Kyvik) provisionChannels(ctx context.Context, config types.AgentConfig, log *slog.Logger) {
	// Legacy path: provision from ChannelMappings.
	// Track which channel types were provisioned here to avoid double-provisioning
	// when an agent also has a dedicated mode field (DiscordMode, SlackMode) set.
	legacyProvisioned := make(map[string]bool)
	for _, mapping := range config.Channels {
		adapter, ok := p.channels[mapping.ChannelType]
		if !ok {
			log.Debug("no adapter registered for channel type", "channel", mapping.ChannelType)
			continue
		}
		if err := adapter.ProvisionAgent(ctx, config); err != nil {
			log.Error("channel provisioning failed", "channel", mapping.ChannelType, "error", err)
		} else {
			log.Info("channel provisioned", "channel", mapping.ChannelType)
			legacyProvisioned[mapping.ChannelType] = true
		}
	}

	// New Slack mode provisioning (SlackManager reads SlackMode internally).
	// Skip if the legacy Channels path already provisioned the slack adapter.
	if !legacyProvisioned["slack"] && (config.SlackMode == types.SlackModePrimary || config.SlackMode == types.SlackModeDedicated) {
		if adapter, ok := p.channels["slack"]; ok {
			if err := adapter.ProvisionAgent(ctx, config); err != nil {
				log.Error("slack provisioning failed", "mode", config.SlackMode, "error", err)
			}
		}
	}

	// Discord mode provisioning (DiscordManager reads DiscordMode internally).
	// Skip if the legacy Channels path already provisioned the discord adapter.
	if !legacyProvisioned["discord"] && (config.DiscordMode == types.DiscordModePrimary || config.DiscordMode == types.DiscordModeDedicated) {
		if adapter, ok := p.channels["discord"]; ok {
			if err := adapter.ProvisionAgent(ctx, config); err != nil {
				log.Error("discord provisioning failed", "mode", config.DiscordMode, "error", err)
			}
		}
	}

	// Always provision the internal adapter for inter-agent messaging.
	if adapter, ok := p.channels["internal"]; ok {
		if err := adapter.ProvisionAgent(ctx, config); err != nil {
			log.Warn("internal channel provisioning failed", "error", err)
		}
	}
}

// deprovisionChannels calls DeprovisionAgent on each registered channel adapter.
func (p *Kyvik) deprovisionChannels(ctx context.Context, agentID string, log *slog.Logger) {
	for name, adapter := range p.channels {
		if err := adapter.DeprovisionAgent(ctx, agentID); err != nil {
			// ErrNotProvisioned is expected if the agent wasn't mapped to this adapter
			if err != types.ErrNotProvisioned {
				log.Error("channel deprovisioning failed", "channel", name, "error", err)
			}
		} else {
			log.Info("channel deprovisioned", "channel", name)
		}
	}
}

// startOutboxConsumer spawns a goroutine that drains the agent's outbox and
// delivers messages to the appropriate channel adapter(s). Without this,
// a full outbox buffer (16 messages) would deadlock the agent goroutine.
func (p *Kyvik) startOutboxConsumer(runner *AgentRunner) {
	ctx, cancel := context.WithCancel(context.Background())
	runner.outboxCancel = cancel
	runner.outboxDone = make(chan struct{})

	go func() {
		log := slog.With("agent_id", runner.config.ID)
		defer close(runner.outboxDone)

		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-runner.outbox:
				if !ok {
					return
				}
				log.Debug("outbox message consumed", "content_len", len(msg.Content))

				// Deliver to each channel adapter that has this agent provisioned
				p.mu.RLock()
				adapters := make([]channels.Adapter, 0, len(p.channels))
				for _, a := range p.channels {
					adapters = append(adapters, a)
				}
				p.mu.RUnlock()

				for _, adapter := range adapters {
					if err := adapter.Send(ctx, runner.config.ID, msg); err != nil {
						// ErrNotProvisioned means this adapter doesn't have the agent mapped — not an error
						if err != types.ErrNotProvisioned {
							log.Error("channel send failed", "channel", adapter.Name(), "error", err)
						}
					} else {
						log.Debug("message sent to channel", "channel", adapter.Name())
					}
				}
			}
		}
	}()
}

// marshalAttachments JSON-encodes attachments for the queue's text column.
// Returns empty string if there are no attachments.
func marshalAttachments(atts []types.Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	data, err := json.Marshal(atts)
	if err != nil {
		return ""
	}
	return string(data)
}

// attachmentMeta returns a JSON string of attachment metadata (no Data field)
// for storage in conversation history.
func attachmentMeta(atts []types.Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	meta := make([]types.Attachment, len(atts))
	for i, a := range atts {
		meta[i] = types.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(data)
}

// isToolRelated400 checks if an error is a 400-status API error likely caused
// by an upstream provider not supporting tool calling for the routed model.
func isToolRelated400(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "status 400") {
		return false
	}
	return strings.Contains(msg, "tool_call_id") ||
		strings.Contains(msg, "tool_call") ||
		strings.Contains(msg, "upstream provider")
}

// dsmlPattern matches DeepSeek internal markup blocks:
//
//	<｜DSML｜function_calls>...</content> or similar self-closing blocks.
var dsmlPattern = regexp.MustCompile(`(?s)<｜DSML｜[^>]*>.*?(?:</content>|<｜/DSML｜>|$)`)

// stripDSMLMarkup removes DeepSeek internal tool markup that leaks as text
// when an upstream provider doesn't support structured tool calling.
func stripDSMLMarkup(content string) string {
	if !strings.Contains(content, "<｜DSML｜") {
		return content
	}
	cleaned := dsmlPattern.ReplaceAllString(content, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "[Tool calling not supported by current provider routing]"
	}
	return cleaned
}

// stripToolMessages removes all tool-role messages and strips ToolCalls from
// assistant messages, converting them to plain text. Used when retrying a
// request without tools after a 400 error.
func stripToolMessages(messages []models.ChatMessage) []models.ChatMessage {
	var result []models.ChatMessage
	for _, m := range messages {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			if len(m.ToolCalls) > 0 {
				if m.Content == "" {
					continue
				}
				result = append(result, models.ChatMessage{
					Role:    "assistant",
					Content: m.Content,
				})
			} else {
				result = append(result, m)
			}
		default:
			result = append(result, m)
		}
	}
	return result
}

// trimOldestToolMessages removes the oldest assistant+tool message pairs from
// the messages slice to free at least targetTokens of estimated token space.
// The system message (index 0) and the last user message are preserved.
func trimOldestToolMessages(messages []models.ChatMessage, targetTokens int) []models.ChatMessage {
	if len(messages) < 3 || targetTokens <= 0 {
		return messages
	}

	// Find removable indices: tool-loop messages between the system prompt
	// and the most recent user message. We scan from index 1 forward and
	// remove assistant/tool pairs oldest-first until we've freed enough.
	freed := 0
	removeSet := make(map[int]bool)

	for i := 1; i < len(messages) && freed < targetTokens; i++ {
		m := messages[i]
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			freed += ctxbudget.EstimateTokens(m.Content)
			removeSet[i] = true
		}
	}

	if len(removeSet) == 0 {
		return messages
	}

	result := make([]models.ChatMessage, 0, len(messages)-len(removeSet))
	for i, m := range messages {
		if !removeSet[i] {
			result = append(result, m)
		}
	}
	return result
}

// GetSystemState retrieves a system-level key/value pair from the store.
func (p *Kyvik) GetSystemState(ctx context.Context, key string) (string, error) {
	return p.store.GetSystemState(ctx, key)
}

// SetSystemState stores a system-level key/value pair.
func (p *Kyvik) SetSystemState(ctx context.Context, key, value string) error {
	return p.store.SetSystemState(ctx, key, value)
}
