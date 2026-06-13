package types

import "errors"

// Sentinel errors for use across Kyvik subsystems.
var (
	ErrNotFound            = errors.New("not found")
	ErrPermissionDenied    = errors.New("permission denied")
	ErrBudgetExceeded      = errors.New("budget exceeded")
	ErrProviderUnavailable = errors.New("provider unavailable")
	ErrAgentNotRunning     = errors.New("agent not running")
	ErrAgentAlreadyRunning = errors.New("agent already running")
	ErrAdapterClosed       = errors.New("adapter closed")
	ErrNotProvisioned      = errors.New("agent not provisioned for channel")
	ErrDecryptionFailed    = errors.New("decryption failed")
	ErrMasterKeyRequired   = errors.New("KYVIK_MASTER_KEY environment variable is required. Run 'make generate-key' to create one.")

	// Circuit breaker errors.
	ErrCircuitBreakerTripped = errors.New("circuit breaker tripped")

	// Vacation mode errors.
	ErrVacationModeActive = errors.New("vacation mode active: agent operations are paused")

	// Schedule errors.
	ErrScheduleNotFound   = errors.New("schedule not found")
	ErrDuplicateHeartbeat = errors.New("agent already has a heartbeat schedule")

	// Worker errors.
	ErrWorkersDisabled    = errors.New("workers not enabled for this agent")
	ErrWorkerLimitReached = errors.New("worker concurrency limit reached")
	ErrWorkerNotFound     = errors.New("worker not found")
	ErrWorkerSpawnDenied  = errors.New("workers cannot spawn other workers")

	// Skill errors.
	ErrSkillNotFound       = errors.New("skill not found")
	ErrSkillAlreadyGranted = errors.New("skill already granted to agent")
	ErrSkillRequirements   = errors.New("agent does not meet skill requirements")

	// Team / messaging errors.
	ErrTeamNotFound        = errors.New("team not found")
	ErrAgentNotInTeam      = errors.New("agent not in team")
	ErrMessageNotPermitted = errors.New("agent not permitted to message target")
	ErrTeamPaused          = errors.New("team communication paused")
	ErrPairedConvNotFound  = errors.New("paired conversation not found")
	ErrPairedConvNotActive = errors.New("paired conversation not active")

	// User / auth errors.
	ErrUserNotFound    = errors.New("user not found")
	ErrGroupNotFound   = errors.New("group not found")
	ErrSessionNotFound = errors.New("session not found")

	// API key errors.
	ErrAPIKeyNotFound    = errors.New("api key not found")
	ErrAPIKeyInactive    = errors.New("api key inactive")
	ErrAPIKeyInvalid     = errors.New("api key invalid")
	ErrRateLimitExceeded = errors.New("rate limit exceeded")

	// Outbound webhook errors.
	ErrOutboundWebhookNotFound = errors.New("outbound webhook not found")

	// Provider errors.
	ErrProviderNotFound = errors.New("provider not found")

	// Workflow errors.
	ErrWorkflowNotFound    = errors.New("workflow not found")
	ErrWorkflowRunNotFound = errors.New("workflow run not found")
)
