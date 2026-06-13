package integrations

import "github.com/kkjorsvik/kyvik/pkg/types"

// NativeToolManifest describes a built-in KTP tool that an agent can use
// without any REST endpoint configuration.
type NativeToolManifest struct {
	Name        string
	DisplayName string
	Description string
	Category    types.IntegrationCategory
	Icon        string
	MinTier     string // minimum agent permission template: "reader", "worker", "operator", "admin"
	Auth        TemplateAuth
}

// builtinNativeTools is the registry of native KTP tools available for installation.
var builtinNativeTools = []*NativeToolManifest{
	// ── Core tools ──
	{
		Name:        "file",
		DisplayName: "File System",
		Description: "Read, write, list, and delete files within the agent's workspace directory.",
		Category:    types.IntCatDeveloperTools,
		Icon:        "file",
		MinTier:     "reader",
	},
	{
		Name:        "memory",
		DisplayName: "Memory",
		Description: "Remember, recall, and forget persistent key-value memories across conversations.",
		Category:    types.IntCatData,
		Icon:        "brain",
		MinTier:     "reader",
	},
	{
		Name:        "scheduler",
		DisplayName: "Scheduler",
		Description: "Create, list, update, and delete scheduled tasks with cron expressions.",
		Category:    types.IntCatData,
		Icon:        "clock",
		MinTier:     "reader",
	},
	{
		Name:        "workflow",
		DisplayName: "Workflows",
		Description: "Create, manage, and execute deterministic tool-chain workflows that automate multi-step operations.",
		Category:    types.IntCatData,
		Icon:        "workflow",
		MinTier:     "writer",
	},
	{
		Name:        "obsidian",
		DisplayName: "Obsidian Vault",
		Description: "Read, write, search, and navigate notes in Obsidian vaults synced via Obsidian Sync.",
		Category:    types.IntCatData,
		Icon:        "notebook",
		MinTier:     "reader",
	},
	{
		Name:        "my_spending",
		DisplayName: "My Spending",
		Description: "Query own token usage and cost summaries across time periods.",
		Category:    types.IntCatMonitoring,
		Icon:        "dollar-sign",
		MinTier:     "reader",
	},
	{
		Name:        "email",
		DisplayName: "Email",
		Description: "Send emails, read inbox, and search messages via configured SMTP/IMAP.",
		Category:    types.IntCatCommunication,
		Icon:        "mail",
		MinTier:     "reader",
	},

	// ── Developer tools ──
	{
		Name:        "github",
		DisplayName: "GitHub",
		Description: "Read repos, list and create issues, and post comments on the GitHub API.",
		Category:    types.IntCatDeveloperTools,
		Icon:        "github",
		MinTier:     "worker",
		Auth: TemplateAuth{
			Type:         "bearer",
			SecretRef:    "github:token",
			Instructions: "Create a GitHub personal access token at https://github.com/settings/tokens with `repo` scope (or finer-grained permissions as needed).",
			SetupURL:     "https://github.com/settings/tokens",
		},
	},
	{
		Name:        "git",
		DisplayName: "Git",
		Description: "Local git operations: clone repos, create branches, commit changes, push, and pull.",
		Category:    types.IntCatDeveloperTools,
		Icon:        "git-branch",
		MinTier:     "worker",
		Auth: TemplateAuth{
			Type:         "bearer",
			SecretRef:    "git:token",
			Instructions: "Provide a token for push/clone operations. Falls back to github:token if not set.",
		},
	},
	{
		Name:        "http",
		DisplayName: "HTTP",
		Description: "Make HTTP requests to allowed external hosts with configurable methods and headers.",
		Category:    types.IntCatData,
		Icon:        "globe",
		MinTier:     "worker",
	},
	{
		Name:        "rest_api",
		DisplayName: "REST API",
		Description: "Call pre-configured REST API endpoints with vault-backed authentication.",
		Category:    types.IntCatData,
		Icon:        "zap",
		MinTier:     "worker",
	},
	{
		Name:        "database",
		DisplayName: "Database",
		Description: "Query pre-configured external databases (PostgreSQL, MySQL, SQL Server, SQLite) with parameterized queries and SQL safety checks.",
		Category:    types.IntCatData,
		Icon:        "database",
		MinTier:     "reader",
	},

	// ── Team tools ──
	{
		Name:        "team.delegate",
		DisplayName: "Team Delegate",
		Description: "Delegate tasks to other agents and receive results asynchronously.",
		Category:    types.IntCatCommunication,
		Icon:        "users",
		MinTier:     "worker",
	},
	{
		Name:        "team.broadcast",
		DisplayName: "Team Broadcast",
		Description: "Broadcast messages to all agents or a filtered set of teammates.",
		Category:    types.IntCatCommunication,
		Icon:        "radio",
		MinTier:     "worker",
	},
	{
		Name:        "team.status",
		DisplayName: "Team Status",
		Description: "Check the status and availability of other agents in the team.",
		Category:    types.IntCatCommunication,
		Icon:        "activity",
		MinTier:     "worker",
	},
	{
		Name:        "team.recall",
		DisplayName: "Team Recall",
		Description: "Recall a previously delegated task or cancel a pending delegation.",
		Category:    types.IntCatCommunication,
		Icon:        "rotate-ccw",
		MinTier:     "worker",
	},

	// ── Execution tools ──
	{
		Name:        "shell",
		DisplayName: "Shell",
		Description: "Execute allowed shell commands in the agent's workspace with configurable command allowlists.",
		Category:    types.IntCatDeveloperTools,
		Icon:        "terminal",
		MinTier:     "operator",
	},
	{
		Name:        "code",
		DisplayName: "Code Runner",
		Description: "Execute code snippets or files in the agent's workspace (Python, Node.js, etc.).",
		Category:    types.IntCatDeveloperTools,
		Icon:        "code",
		MinTier:     "operator",
	},
	{
		Name:        "docker",
		DisplayName: "Docker",
		Description: "Build, run, stop, and manage Docker containers with workspace-confined volumes and default resource limits.",
		Category:    types.IntCatDeveloperTools,
		Icon:        "box",
		MinTier:     "operator",
	},
	{
		Name:        "browser",
		DisplayName: "Browser",
		Description: "Fetch web pages, take screenshots, extract links, and search the web.",
		Category:    types.IntCatData,
		Icon:        "globe",
		MinTier:     "operator",
	},

	// ── System tools ──
	{
		Name:        "system_status",
		DisplayName: "System Status",
		Description: "View agent statuses, system overview, spending summaries, and recent errors.",
		Category:    types.IntCatMonitoring,
		Icon:        "monitor",
		MinTier:     "guide",
	},
	{
		Name:        "hostfs",
		DisplayName: "Host Filesystem",
		Description: "Read and write files on the host filesystem outside the workspace (admin only, allowlist-controlled).",
		Category:    types.IntCatDeveloperTools,
		Icon:        "hard-drive",
		MinTier:     "admin",
	},
}

// BuiltinNativeTools returns a copy of the built-in native tool registry.
func BuiltinNativeTools() []*NativeToolManifest {
	out := make([]*NativeToolManifest, len(builtinNativeTools))
	copy(out, builtinNativeTools)
	return out
}

// GetNative returns the NativeToolManifest for the given tool name, or nil if not found.
func GetNative(name string) *NativeToolManifest {
	for _, m := range builtinNativeTools {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// AvailableNative returns all built-in native tool manifests.
func AvailableNative() []*NativeToolManifest {
	return BuiltinNativeTools()
}

// NativeInstallRequest contains the parameters for granting a native tool to an agent.
type NativeInstallRequest struct {
	AgentID     string
	ToolName    string
	AuthSecret  string
	InstalledBy string
}
