// Package store defines the storage interface for Kyvik.
// Implementations must be swappable — PostgreSQL is the production backend.
// 
package store

import (
	"context"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Store defines the data persistence contract for Kyvik.
// This is a thin data-access layer — domain logic belongs in
// audit.Logger, spending.Tracker, and other subsystem interfaces.
type Store interface {
	// Agent operations
	CreateAgent(ctx context.Context, config types.AgentConfig) error
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
	ListAgents(ctx context.Context) ([]types.AgentConfig, error)
	UpdateAgent(ctx context.Context, config types.AgentConfig) error
	DeleteAgent(ctx context.Context, id string) error

	// Agent state management
	SetDesiredState(ctx context.Context, agentID string, state types.DesiredState) error
	SetActualState(ctx context.Context, agentID string, state types.AgentStatus, lastError string) error

	// Audit persistence
	InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error
	InsertAuditEntries(ctx context.Context, entries []types.AuditEntry) error
	ListAuditEntries(ctx context.Context, filter audit.Filter) ([]types.AuditEntry, error)

	// Usage persistence
	InsertUsageRecord(ctx context.Context, agentID string, tokensIn, tokensOut int64, cost float64,
		model, modelSlot, routedBy, provider, parentAgentID string) error
	AggregateUsage(ctx context.Context, agentID string, period string) (*spending.Summary, error)
	AggregateSlotUsage(ctx context.Context, agentID, period string) ([]spending.SlotUsageSummary, error)
	AggregateProviderUsage(ctx context.Context, agentID, period string) ([]spending.ProviderUsageSummary, error)

	// Security events
	InsertSecurityEvent(ctx context.Context, event types.SecurityEvent) error
	QuerySecurityEvents(ctx context.Context, agentID string, limit int) ([]types.SecurityEvent, error)

	// System state (generic key-value for global flags)
	GetSystemState(ctx context.Context, key string) (string, error)
	SetSystemState(ctx context.Context, key, value string) error

	// Alert acknowledgments
	AcknowledgeAlert(ctx context.Context, sourceType, sourceID string) error
	IsAlertAcknowledged(ctx context.Context, sourceType, sourceID string) (bool, error)
	ListAcknowledgedAlerts(ctx context.Context) (map[string]time.Time, error)

	// Security events cross-agent query
	QueryAllSecurityEvents(ctx context.Context, severity string, limit int) ([]types.SecurityEvent, error)

	// Schedule operations
	CreateSchedule(ctx context.Context, s types.Schedule) error
	GetSchedule(ctx context.Context, id string) (*types.Schedule, error)
	UpdateSchedule(ctx context.Context, s types.Schedule) error
	DeleteSchedule(ctx context.Context, id string) error
	ListSchedules(ctx context.Context, agentID string) ([]types.Schedule, error)
	ListSchedulesByType(ctx context.Context, agentID string, schedType string) ([]types.Schedule, error)
	ListAllEnabledSchedules(ctx context.Context) ([]types.Schedule, error)
	DeleteSchedulesByAgent(ctx context.Context, agentID string) error

	// Skill grant operations
	GrantSkill(ctx context.Context, grant types.SkillGrant) error
	RevokeSkill(ctx context.Context, agentID, skillName string) error
	ListSkillGrants(ctx context.Context, agentID string) ([]types.SkillGrant, error)
	DeleteSkillGrantsByAgent(ctx context.Context, agentID string) error

	// Team operations
	CreateTeam(ctx context.Context, team types.Team) error
	GetTeam(ctx context.Context, id string) (*types.Team, error)
	UpdateTeam(ctx context.Context, team types.Team) error
	DeleteTeam(ctx context.Context, id string) error
	ListTeams(ctx context.Context) ([]types.Team, error)
	GetTeamByAgent(ctx context.Context, agentID string) (*types.Team, error)

	// Outbound webhook operations
	CreateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error
	GetOutboundWebhook(ctx context.Context, id string) (*types.OutboundWebhook, error)
	UpdateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error
	DeleteOutboundWebhook(ctx context.Context, id string) error
	ListOutboundWebhooks(ctx context.Context, agentID string) ([]types.OutboundWebhook, error)
	ListAllEnabledOutboundWebhooks(ctx context.Context) ([]types.OutboundWebhook, error)

	// Webhook delivery operations
	InsertWebhookDelivery(ctx context.Context, d types.WebhookDelivery) error
	ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]types.WebhookDelivery, error)
	ListPendingRetries(ctx context.Context) ([]types.WebhookDelivery, error)
	UpdateDeliveryStatus(ctx context.Context, id string, status types.WebhookDeliveryStatus, httpCode int, responseBody, errMsg string) error
	PruneWebhookDeliveries(ctx context.Context, olderThan time.Duration) (int64, error)

	// Provider operations
	CreateProvider(ctx context.Context, p types.ProviderRecord) error
	GetProvider(ctx context.Context, id string) (*types.ProviderRecord, error)
	UpdateProvider(ctx context.Context, p types.ProviderRecord) error
	DeleteProvider(ctx context.Context, id string) error
	ListProviders(ctx context.Context) ([]types.ProviderRecord, error)

	// Discord authorization operations
	CreateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
	GetDiscordAuth(ctx context.Context, agentID, discordUserID string) (*types.DiscordAuthorization, error)
	GetDiscordAuthByCode(ctx context.Context, code string) (*types.DiscordAuthorization, error)
	UpdateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
	ListDiscordAuths(ctx context.Context, agentID string) ([]types.DiscordAuthorization, error)
	DeleteDiscordAuth(ctx context.Context, id string) error

	// Workflows
	CreateWorkflow(ctx context.Context, w types.Workflow) error
	GetWorkflow(ctx context.Context, id string) (*types.Workflow, error)
	GetWorkflowByName(ctx context.Context, agentID, name string) (*types.Workflow, error)
	UpdateWorkflow(ctx context.Context, w types.Workflow) error
	DeleteWorkflow(ctx context.Context, id string) error
	ListWorkflows(ctx context.Context, agentID string) ([]types.Workflow, error)

	// Workflow Runs
	CreateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
	GetWorkflowRun(ctx context.Context, id string) (*types.WorkflowRun, error)
	UpdateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
	ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]types.WorkflowRun, error)

	// Cluster coordination (requires PostgreSQL)
	RegisterNode(ctx context.Context, node types.NodeInfo) error
	UpdateHeartbeat(ctx context.Context, nodeID string, capacity types.NodeCapacity) error
	ListNodes(ctx context.Context) ([]types.NodeInfo, error)
	GetDeadNodes(ctx context.Context, timeout time.Duration) ([]types.NodeInfo, error)
	SetNodeStatus(ctx context.Context, nodeID, status string) error
	DeleteNode(ctx context.Context, nodeID string) error

	AssignAgent(ctx context.Context, agentID, nodeID string) error
	GetAssignment(ctx context.Context, agentID string) (*types.Assignment, error)
	GetNodeAgents(ctx context.Context, nodeID string) ([]types.Assignment, error)
	GetOrphanedAgents(ctx context.Context, nodeID string) ([]types.Assignment, error)
	DeleteAssignment(ctx context.Context, agentID string) error

	// Lifecycle
	Close() error
}
