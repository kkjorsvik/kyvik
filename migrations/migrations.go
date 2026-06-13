// Package migrations embeds the SQL migration files for use by store backends.
package migrations

import _ "embed"

//go:embed 001_core.sql
var CoreSchema string

//go:embed 001_audit.sql
var AuditSchema string

//go:embed 002_secrets.sql
var SecretsSchema string

//go:embed 003_queue.sql
var QueueSchema string

//go:embed 004_agent_state.sql
var AgentStateSchema string

//go:embed 005_soul_identity.sql
var SoulIdentitySchema string

//go:embed 006_history.sql
var HistorySchema string

//go:embed 007_memory.sql
var MemorySchema string

//go:embed 009_channel_config.sql
var ChannelConfigSchema string

//go:embed 010_history_attachments.sql
var HistoryAttachmentsSchema string

//go:embed 011_model_slots.sql
var ModelSlotsSchema string

//go:embed 012_spending_slots.sql
var SpendingSlotsSchema string

//go:embed 013_worker_config.sql
var WorkerConfigSchema string

//go:embed 014_agent_capabilities.sql
var AgentCapabilitiesSchema string

//go:embed 015_tool_audit.sql
var ToolAuditSchema string

//go:embed 016_workspaces.sql
var WorkspacesSchema string

//go:embed 017_host_paths.sql
var HostPathsSchema string

//go:embed 018_security_events.sql
var SecurityEventsSchema string

//go:embed 019_web_conversations.sql
var WebConversationsSchema string

//go:embed 020_circuit_breaker.sql
var CircuitBreakerSchema string

//go:embed 021_system_state.sql
var SystemStateSchema string

//go:embed 022_alert_acknowledgments.sql
var AlertAcknowledgmentsSchema string

//go:embed 023_schedules.sql
var SchedulesSchema string

//go:embed 024_conversation_archive.sql
var ConversationArchiveSchema string

//go:embed 025_skill_grants.sql
var SkillGrantsSchema string

//go:embed 026_teams.sql
var TeamsSchema string

//go:embed 027_internal_message_acks.sql
var InternalMessageAcksSchema string

//go:embed 028_queue_message_type.sql
var QueueMessageTypeSchema string

//go:embed 029_pricing_catalog.sql
var PricingCatalogSchema string

//go:embed 030_users_sessions_groups.sql
var UsersSessionsGroupsSchema string

//go:embed 031_api_keys.sql
var APIKeysSchema string

//go:embed 032_agent_templates.sql
var AgentTemplatesSchema string

//go:embed 033_host_filesystem.sql
var HostFilesystemSchema string

//go:embed 034_webhook_inbound.sql
var WebhookInboundSchema string

//go:embed 035_outbound_webhooks.sql
var OutboundWebhooksSchema string

//go:embed 036_rest_api_endpoints.sql
var RESTAPIEndpointsSchema string

//go:embed 037_guide_agent.sql
var GuideAgentSchema string

//go:embed 038_providers.sql
var ProvidersSchema string

//go:embed 039_discord_fields.sql
var DiscordFieldsSchema string

//go:embed 040_discord_auth.sql
var DiscordAuthSchema string

//go:embed 041_http_allowed_hosts.sql
var HTTPAllowedHostsSchema string

//go:embed 042_timestamp_messages.sql
var TimestampMessagesSchema string

//go:embed 043_audit_risk_level.sql
var AuditRiskLevelSchema string

//go:embed 044_conversation_compression.sql
var ConversationCompressionSchema string

//go:embed 045_workflows.sql
var WorkflowsSchema string

//go:embed 046_cluster.sql
var ClusterSchema string

//go:embed 047_memory_status.sql
var MemoryStatusSchema string

//go:embed 048_obsidian_vaults.sql
var ObsidianVaultsSchema string

//go:embed 049_attachment_max_size.sql
var AttachmentMaxSizeSchema string

//go:embed 050_agents_missing_columns.sql
var AgentsMissingColumnsSchema string

//go:embed 051_queue_history_missing_columns.sql
var QueueHistoryMissingColumnsSchema string

//go:embed 052_agents_memory_cluster_columns.sql
var AgentsMemoryClusterColumnsSchema string

// InitialSchema is the combined schema for backward compatibility.
var InitialSchema = CoreSchema + "\n" + AuditSchema
