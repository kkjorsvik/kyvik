// Package bootstrap wires together all Kyvik subsystems from configuration.
// It is used by cmd/kyvik/main.go to keep the entrypoint small.
package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/attachments"
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/authprovider/delegated"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/authprovider/oidc"
	"github.com/kkjorsvik/kyvik/internal/backup"
	"github.com/kkjorsvik/kyvik/internal/channelmgr"
	"github.com/kkjorsvik/kyvik/internal/channels/busadapter"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/cluster"
	"github.com/kkjorsvik/kyvik/internal/compression"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/ctxbudget"
	"github.com/kkjorsvik/kyvik/internal/feedback"
	"github.com/kkjorsvik/kyvik/internal/guide"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/integrations"
	"github.com/kkjorsvik/kyvik/internal/keymanager"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
	"github.com/kkjorsvik/kyvik/internal/netproxy"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	obsidianpkg "github.com/kkjorsvik/kyvik/internal/obsidian"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	providerpkg "github.com/kkjorsvik/kyvik/internal/providers"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/retention"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/sandbox"
	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/internal/skills/builtins"
	"github.com/kkjorsvik/kyvik/internal/spending"
	storepkg "github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/internal/templates"
	templatebuiltins "github.com/kkjorsvik/kyvik/internal/templates/builtins"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/internal/tools/browser"
	"github.com/kkjorsvik/kyvik/internal/tools/email"
	"github.com/kkjorsvik/kyvik/internal/tools/file"
	"github.com/kkjorsvik/kyvik/internal/tools/hostfs"
	"github.com/kkjorsvik/kyvik/internal/tools/restapi"
	schedulertool "github.com/kkjorsvik/kyvik/internal/tools/scheduler"
	workflowtool "github.com/kkjorsvik/kyvik/internal/tools/workflow"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/internal/workers"
	workflowengine "github.com/kkjorsvik/kyvik/internal/workflow"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/kkjorsvik/kyvik/web"
)

// appStore composes the store interfaces required during bootstrap.
type appStore interface {
	storepkg.Store
	spending.SpendingStore
	permissions.PermissionStore
	users.Store
	templates.Store
	apikeys.Store
}

// noopRegistry satisfies tools.Registry with no tools registered.
type noopRegistry struct{}

func (n *noopRegistry) Register(_ tools.Tool) error      { return nil }
func (n *noopRegistry) Get(_ string) (tools.Tool, error) { return nil, types.ErrNotFound }
func (n *noopRegistry) List() []tools.Declaration        { return nil }
func (n *noopRegistry) GetDeclaration(_ string) (*tools.Declaration, error) {
	return nil, types.ErrNotFound
}

// Result holds everything the caller (main.go) needs after bootstrap completes.
type Result struct {
	// Kyvik is the core runtime.
	Kyvik *core.Kyvik

	// Handler is the fully wired HTTP handler (routes + middleware).
	Handler http.Handler

	// Cfg is the loaded configuration.
	Cfg *config.Config

	// Closers are functions that should be deferred in main.
	Closers []func()

	// PostStart are functions to call after kyvik.Start() succeeds.
	PostStart []func(ctx context.Context)

	// ClusterManager for startup and shutdown.
	ClusterManager cluster.Manager

	// Scheduler, if enabled, for post-start.
	Scheduler *scheduler.Scheduler

	// Queue for replay after start.
	Queue *queue.PostgresQueue

	// ChannelManager for loading adapters after start.
	ChannelManager *channelmgr.Manager

	// Banner fields for the startup log.
	ProviderNames    []string
	Channels         []string
	KTPRegistry      *ktp.Registry
	SandboxMgr       *sandbox.Manager
	AllowUnrestricted bool
	NotifierType     string
	SkillCount       int
	IntegrationMgr   *integrations.Manager
	ObsidianMgr      *obsidianpkg.VaultManager
	TeamCount        int
	SeparateAuditCloser interface{ Close() error }
}

// Build wires together all Kyvik subsystems from the given config.
// It returns a Result containing everything the caller needs to run the server.
func Build(cfg *config.Config, version string, logStage func(string)) (*Result, error) {
	res := &Result{Cfg: cfg}

	// Validate host access configuration at startup.
	hostDiag := config.ValidateHostAccess(cfg)
	hostDiag.LogDiagnostic()

	// ── Database ──────────────────────────────────────────────────────
	var (
		store appStore
		db    *sql.DB
	)
	var auditStore audit.AuditStore

	switch cfg.Storage.Driver {
	case "postgres":
		pgStore, err := postgres.New(cfg.Storage.Postgres.DSN, postgres.StoreOptions{
			MaxConnections: cfg.Storage.MaxConnections,
		})
		if err != nil {
			return nil, fmt.Errorf("open PostgreSQL: %w", err)
		}
		logStage("postgres store opened")
		store = pgStore
		db = pgStore.DB()
		auditStore = pgStore
		if cfg.Storage.SeparateAuditDB {
			log.Printf("Warning: storage.separate_audit_db is ignored for postgres")
		}
	default:
		return nil, fmt.Errorf("unsupported storage.driver %q", cfg.Storage.Driver)
	}

	// ── Audit & Spending ──────────────────────────────────────────────
	auditLogger := audit.NewStoreLogger(auditStore, cfg.Storage.WriteBatchMS)
	spendingTracker := spending.NewStoreTracker(store, auditLogger, cfg.Models.OpenRouter.DefaultModel)

	// ── Secrets ───────────────────────────────────────────────────────
	masterKey, err := secrets.LoadMasterKey()
	if err != nil {
		return nil, fmt.Errorf("secrets vault: %w", err)
	}
	vault := secrets.NewVault(db, masterKey, auditLogger)

	// ── Permissions ───────────────────────────────────────────────────
	gate := permissions.NewStoreGate(store, auditLogger, "configs/templates")
	gate.SetGuideMode(cfg.Guide.Mode)

	// ── KTP Tool System ───────────────────────────────────────────────
	ktpRegistry := ktp.NewRegistry()
	ktpAuditLogger := ktp.NewStoreAuditLogger(store)
	ktpGate := ktp.NewPermissionGate(store, ktpAuditLogger)
	if cfg.Sandbox.AllowUnrestricted != nil && *cfg.Sandbox.AllowUnrestricted {
		ktpGate.SetAllowUnrestricted(true)
	}
	ktpExecutor := ktp.NewExecutor(ktpRegistry, ktpGate, ktpAuditLogger, ktp.ExecutorConfig{
		DefaultTimeout: time.Duration(cfg.Sandbox.ToolTimeoutSeconds) * time.Second,
	})

	// ── Sandbox ───────────────────────────────────────────────────────
	var sandboxMgr *sandbox.Manager
	sbMgrCfg := sandbox.ManagerConfig{
		WorkspaceRoot: cfg.Sandbox.WorkspaceRoot,
		RunnerPath:    cfg.Sandbox.RunnerPath,
		Defaults: sandbox.SandboxConfig{
			MaxMemoryMB:    cfg.Sandbox.MaxMemoryMB,
			MaxCPUPercent:  cfg.Sandbox.MaxCPUPercent,
			TimeoutSeconds: cfg.Sandbox.TimeoutSeconds,
			MaxOutputBytes: cfg.Sandbox.MaxOutputBytes,
		},
	}
	if mgr, err := sandbox.NewManager(sbMgrCfg); err != nil {
		log.Printf("Warning: Sandbox manager not available: %v", err)
	} else {
		sandboxMgr = mgr
	}

	// Network proxy for sandbox outbound access control.
	if cfg.NetProxy.Enabled && sandboxMgr != nil {
		proxyResolver := func(sandboxID string) (*netproxy.AgentInfo, error) {
			sb, ok := sandboxMgr.GetBySandboxID(sandboxID)
			if !ok {
				return nil, fmt.Errorf("unknown sandbox: %s", sandboxID)
			}
			agent, err := store.GetAgent(context.Background(), sb.AgentID)
			if err != nil {
				return nil, err
			}
			tier := ktp.ResolveAgentTier(agent.Template)
			var hosts []string
			if len(agent.HTTPAllowedHosts) > 0 {
				hosts = agent.HTTPAllowedHosts
			}
			return &netproxy.AgentInfo{
				AgentID:      agent.ID,
				Tier:         tier,
				AllowedHosts: hosts,
			}, nil
		}

		proxy, err := netproxy.New(netproxy.Config{
			ListenAddr:   cfg.NetProxy.ListenAddr,
			ResolveAgent: proxyResolver,
		})
		if err != nil {
			return nil, fmt.Errorf("start network proxy: %w", err)
		}
		res.Closers = append(res.Closers, func() { proxy.Close() })
		sandboxMgr.SetProxyAddr(proxy.Addr())
		log.Printf("Network proxy started on %s", proxy.Addr())
	}

	// Wire sandbox adapter into KTP executor.
	if sandboxMgr != nil {
		sbAdapter := sandbox.NewKTPAdapter(sandboxMgr)
		ktpExecutor.SetSandbox(sbAdapter)
	}
	ktpExecutor.SetSecretResolver(vault)

	// ── Core Runtime ──────────────────────────────────────────────────
	kyvik := core.New(store, gate, sandboxMgr, auditLogger, &noopRegistry{}, spendingTracker)
	kyvik.SetKTPRegistry(ktpRegistry)
	kyvik.SetKTPExecutor(ktpExecutor)

	allowUnrestricted := cfg.Sandbox.AllowUnrestricted != nil && *cfg.Sandbox.AllowUnrestricted
	kyvik.SetHostAccessConfig(cfg.Sandbox.HostAccess, allowUnrestricted)

	// ── Queue ─────────────────────────────────────────────────────────
	queueCfg := queue.Config{
		Depth:               cfg.Queue.Depth,
		FullBehavior:        queue.FullBehavior(cfg.Queue.FullBehavior),
		PriorityUsers:       cfg.Queue.PriorityUsers,
		StaleTimeoutSeconds: cfg.Queue.StaleTimeoutSeconds,
		RetentionHours:      cfg.Queue.RetentionHours,
	}
	msgQueue, err := queue.NewPostgresQueue(db, "", cfg.Storage.Postgres.DSN, queueCfg)
	if err != nil {
		return nil, fmt.Errorf("create queue: %w", err)
	}
	kyvik.SetQueue(msgQueue)

	// ── History & Memory ──────────────────────────────────────────────
	historyStore := history.New(db)
	kyvik.SetHistory(historyStore)
	kyvik.SetConversationStore(historyStore)

	memoryStore := memory.New(db)
	kyvik.SetMemory(memoryStore)

	// ── Teams ─────────────────────────────────────────────────────────
	internalBus := teams.NewBus(db, auditLogger)
	kyvik.SetInternalBus(internalBus)
	teamManager := teams.NewManager(store, internalBus, auditLogger)
	kyvik.SetTeamManager(teamManager)
	pairedOrch := teams.NewPairedOrchestrator(internalBus, store, db, auditLogger)
	kyvik.SetPairedOrchestrator(pairedOrch)

	// ── Obsidian ──────────────────────────────────────────────────────
	obsidianStore := obsidianpkg.NewDBVaultStore(db)
	obsidianMgr := obsidianpkg.NewManager(obsidianStore, vault)
	if err := obsidianMgr.Start(context.Background()); err != nil {
		log.Printf("Warning: obsidian vault manager start failed: %v", err)
	}
	kyvik.SetObsidianVaultManager(obsidianMgr)

	// ── Register Built-in KTP Tools ───────────────────────────────────
	toolOpts := tools.RegistrationOptions{
		WorkspaceFunc: func(agentID string) (string, error) {
			return filepath.Join(cfg.Sandbox.WorkspaceRoot, agentID), nil
		},
		MemoryStore: memoryStore,
		AllowedHostsFunc: func(agentID string) ([]string, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			return agent.HTTPAllowedHosts, nil
		},
		AllowedCommandsFunc: func(agentID string) ([]string, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			return agent.ShellAllowedCommands, nil
		},
		AgentTierFunc: func(agentID string) (string, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return "", err
			}
			return ktp.ResolveAgentTier(agent.Template), nil
		},
		HostPathsFunc: func(agentID string) (*file.HostPathConfig, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			var hpc file.HostPathConfig
			if agent.HostPaths != nil {
				hpc.Read = append(hpc.Read, agent.HostPaths.Read...)
				hpc.Write = append(hpc.Write, agent.HostPaths.Write...)
				hpc.Deny = append(hpc.Deny, agent.HostPaths.Deny...)
			}
			for _, mp := range cfg.Sandbox.ExtraPaths.ReadWrite {
				hpc.Read = append(hpc.Read, mp)
				hpc.Write = append(hpc.Write, mp)
			}
			hpc.Read = append(hpc.Read, cfg.Sandbox.ExtraPaths.ReadOnly...)
			if len(hpc.Read) == 0 && len(hpc.Write) == 0 && len(hpc.Deny) == 0 {
				return nil, nil
			}
			return &hpc, nil
		},
		TeamManager: teamManager,
		InternalBus: internalBus,
		AgentLookup: func(ctx context.Context, id string) (*types.AgentConfig, error) {
			return store.GetAgent(ctx, id)
		},
		EndpointConfigsFunc: restapi.EndpointConfigsFunc(func(agentID string) ([]types.RESTAPIEndpoint, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			if agent.RESTAPIEndpointsJSON == "" {
				return nil, nil
			}
			var endpoints []types.RESTAPIEndpoint
			if err := json.Unmarshal([]byte(agent.RESTAPIEndpointsJSON), &endpoints); err != nil {
				return nil, fmt.Errorf("unmarshal rest_api_endpoints: %w", err)
			}
			return endpoints, nil
		}),
		RESTAllowedHostsFunc: restapi.AllowedHostsFunc(func(agentID string) ([]string, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			return agent.HTTPAllowedHosts, nil
		}),
		SecretResolverFunc: restapi.SecretResolverFunc(func(ctx context.Context, agentID, teamID, key string) (string, error) {
			if v, err := vault.Get(ctx, agentID, key); err == nil {
				return v, nil
			}
			if teamID != "" {
				if v, err := vault.Get(ctx, teamID, key); err == nil {
					return v, nil
				}
			}
			return vault.Get(ctx, "global", key)
		}),
		StatusStore: store,
		ObsidianVaultPathFunc: func(ctx context.Context, name string) (string, error) {
			return obsidianMgr.VaultPath(ctx, name)
		},
		ObsidianVaultAccessFunc: func(agentID string) ([]string, error) {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil {
				return nil, err
			}
			return agent.ObsidianVaults, nil
		},
	}
	if err := tools.RegisterBuiltinTools(ktpRegistry, toolOpts); err != nil {
		return nil, fmt.Errorf("register built-in tools: %w", err)
	}

	// Host filesystem tool (power tier).
	hostfsTool := hostfs.New(hostfs.Config{
		MaxReadBytes:   cfg.HostFilesystem.MaxReadBytes,
		MaxWriteBytes:  cfg.HostFilesystem.MaxWriteBytes,
		MaxListDepth:   cfg.HostFilesystem.MaxListDepth,
		MaxListEntries: cfg.HostFilesystem.MaxListEntries,
	}, hostfs.WithAllowlistFunc(func(agentID string) (*hostfs.HostPathConfig, error) {
		agent, err := store.GetAgent(context.Background(), agentID)
		if err != nil {
			return nil, err
		}
		var hpc hostfs.HostPathConfig
		if agent.HostFilesystem != nil {
			for _, entry := range agent.HostFilesystem.Allowlist {
				switch strings.ToLower(strings.TrimSpace(entry.Access)) {
				case "read":
					hpc.Read = append(hpc.Read, entry.Path)
				case "write":
					hpc.Write = append(hpc.Write, entry.Path)
				}
			}
		}
		if agent.HostPaths != nil {
			hpc.Read = append(hpc.Read, agent.HostPaths.Read...)
			hpc.Write = append(hpc.Write, agent.HostPaths.Write...)
			hpc.Deny = append(hpc.Deny, agent.HostPaths.Deny...)
		}
		for _, mp := range cfg.Sandbox.ExtraPaths.ReadWrite {
			hpc.Read = append(hpc.Read, mp)
			hpc.Write = append(hpc.Write, mp)
		}
		hpc.Read = append(hpc.Read, cfg.Sandbox.ExtraPaths.ReadOnly...)
		if len(hpc.Read) == 0 && len(hpc.Write) == 0 && len(hpc.Deny) == 0 {
			return nil, nil
		}
		return &hpc, nil
	}), hostfs.WithAuditLogger(auditLogger))
	if err := ktpRegistry.Register(hostfsTool); err != nil {
		return nil, fmt.Errorf("register hostfs tool: %w", err)
	}

	// Browser tool (headless web renderer).
	browserTool := browser.New(browser.Config{
		TimeoutSeconds:      cfg.Browser.TimeoutSeconds,
		SettleMillis:        cfg.Browser.SettleMillis,
		MaxTextBytes:        cfg.Browser.MaxTextBytes,
		ViewportWidth:       cfg.Browser.ViewportWidth,
		ViewportHeight:      cfg.Browser.ViewportHeight,
		MaxViewportWidth:    cfg.Browser.MaxViewportWidth,
		MaxViewportHeight:   cfg.Browser.MaxViewportHeight,
		MaxScreenshotWidth:  cfg.Browser.MaxScreenshotWidth,
		MaxScreenshotHeight: cfg.Browser.MaxScreenshotHeight,
		MaxResults:          cfg.Browser.MaxResults,
	}, browser.WithAllowedHostsFunc(func(agentID string) ([]string, error) {
		agent, err := store.GetAgent(context.Background(), agentID)
		if err != nil {
			return nil, err
		}
		return agent.HTTPAllowedHosts, nil
	}), browser.WithAuditLogger(auditLogger))
	if err := ktpRegistry.Register(browserTool); err != nil {
		return nil, fmt.Errorf("register browser tool: %w", err)
	}
	res.Closers = append(res.Closers, func() { browserTool.Close() })

	// Email tool (SMTP/IMAP).
	emailTool := email.New(func(ctx context.Context, agentID, teamID, key string) (string, error) {
		if v, err := vault.Get(ctx, agentID, key); err == nil {
			return v, nil
		}
		if teamID != "" {
			if v, err := vault.Get(ctx, teamID, key); err == nil {
				return v, nil
			}
		}
		return vault.Get(ctx, "global", key)
	}, email.Config{})
	if err := ktpRegistry.Register(emailTool); err != nil {
		return nil, fmt.Errorf("register email tool: %w", err)
	}

	// ── Context Assembler ─────────────────────────────────────────────
	assembler := ctxbudget.New(memoryStore, historyStore)
	kyvik.SetAssembler(assembler)

	// ── Skills ────────────────────────────────────────────────────────
	var skillCount int
	if cfg.Skills.Enabled != nil && *cfg.Skills.Enabled {
		skillLoader, err := skills.NewLoader(cfg.Skills.SkillsDir)
		if err != nil {
			log.Printf("Warning: Skill loader failed: %v", err)
		} else {
			n, err := builtins.Install(skillLoader.BaseDir())
			if err != nil {
				log.Printf("Warning: Built-in skill install failed: %v", err)
			} else if n > 0 {
				log.Printf("Installed %d built-in skill(s)", n)
			}

			sm, err := skills.NewManager(skillLoader, store)
			if err != nil {
				log.Printf("Warning: Skill manager catalog load failed: %v", err)
				sm = skills.NewEmptyManager(skillLoader, store)
			}
			kyvik.SetSkillManager(sm)
			assembler.SetSkillsProvider(sm)
			skillCount = len(sm.Available())
		}
	}

	// ── Model Providers ───────────────────────────────────────────────
	providerEnc := providerpkg.NewEncryptor(masterKey)
	providerMgr := providerpkg.NewManager(store, providerEnc, kyvik)
	providerNames, err := providerMgr.SyncProviders(context.Background(), cfg.Models)
	if err != nil {
		log.Printf("Warning: provider sync failed: %v", err)
	}

	// OpenRouter per-agent key provisioning.
	var km *keymanager.KeyManager
	var channels []string
	if cfg.Models.OpenRouter.APIKey != "" {
		orProvisioningKey := cfg.Models.OpenRouter.ProvisioningKey
		if orProvisioningKey == "" {
			if v, err := vault.Get(context.Background(), "global", "openrouter:provisioning_key"); err == nil {
				orProvisioningKey = v
			}
		}
		if orProvisioningKey != "" {
			mgmtClient := openrouter.NewManagementClient(orProvisioningKey)
			km = keymanager.New(mgmtClient, vault, auditLogger)
			kyvik.SetKeyProvisioner(km)
			log.Println("OpenRouter per-agent key provisioning enabled")
		}
	}

	if len(providerNames) == 0 {
		log.Println("Warning: No model providers available")
	}

	// ── Channel Adapters ──────────────────────────────────────────────
	webuiAdapter := webui.New()
	kyvik.RegisterChannel(webuiAdapter)
	channels = append(channels, "webui")

	attachmentSvc := attachments.New(
		func(agentID string) (string, error) {
			return filepath.Join(cfg.Sandbox.WorkspaceRoot, agentID), nil
		},
		func(agentID string) int64 {
			agent, err := store.GetAgent(context.Background(), agentID)
			if err != nil || agent.AttachmentMaxSizeMB <= 0 {
				return 25 * 1024 * 1024
			}
			return int64(agent.AttachmentMaxSizeMB) * 1024 * 1024
		},
	)

	chMgr := channelmgr.NewManager(vault, kyvik, store, cfg.Channels.Slack, cfg.Channels.Discord)
	chMgr.SetDiscordAuthorizer(store)
	chMgr.SetAttachmentService(attachmentSvc)

	// Internal bus adapter.
	assembler.SetTeamContextProvider(teamManager)
	internalAdapter := busadapter.New(internalBus)
	internalAdapter.SetConfigLookup(func(ctx context.Context, id string) (*types.AgentConfig, error) {
		return store.GetAgent(ctx, id)
	})
	internalAdapter.SetTeamLookup(teamManager.GetTeamForAgent)
	internalAdapter.SetAuditLogger(auditLogger)
	kyvik.RegisterChannel(internalAdapter)
	channels = append(channels, "internal")

	// ── Spending Limits ───────────────────────────────────────────────
	if cfg.Spending.MaxSpendPerDay > 0 || cfg.Spending.MaxSpendPerMonth > 0 ||
		cfg.Spending.MaxTokensPerDay > 0 || cfg.Spending.MaxTokensPerMonth > 0 {
		ctx := context.Background()
		if err := spendingTracker.SetGlobalLimit(ctx, cfg.Spending); err != nil {
			log.Printf("Warning: Failed to set global spending limits: %v", err)
		}
	}

	// ── Notifications ─────────────────────────────────────────────────
	var notifier notifications.Notifier
	notifierType := "log"

	slackBotToken := cfg.Channels.Slack.BotToken
	if slackBotToken == "" {
		slackBotToken = os.Getenv("KYVIK_SLACK_BOT_TOKEN")
	}

	if cfg.Channels.Slack.Enabled && slackBotToken != "" {
		eventsConfig := notifications.EventsConfig{
			CircuitBreaker:    cfg.Notifications.Events.CircuitBreaker,
			AgentError:        cfg.Notifications.Events.AgentError,
			SpendingThreshold: cfg.Notifications.Events.SpendingThreshold,
			BackupStatus:      cfg.Notifications.Events.BackupStatus,
			SecurityAlerts:    cfg.Notifications.Events.SecurityAlerts,
			KeyFailure:        cfg.Notifications.Events.KeyFailure,
			ChannelFailure:    cfg.Notifications.Events.ChannelFailure,
		}
		sn := notifications.NewSlackNotifier(slackBotToken, cfg.Notifications.SlackChannel, eventsConfig)
		if err := sn.Start(); err != nil {
			log.Printf("Warning: Slack notifier failed to start: %v", err)
			notifier = notifications.NewLogNotifier()
		} else {
			notifier = sn
			notifierType = "slack (" + cfg.Notifications.SlackChannel + ")"
		}
	} else {
		notifier = notifications.NewLogNotifier()
	}

	// Outbound webhook dispatcher wraps notifier in MultiNotifier.
	webhookDispatcher := webhooks.NewDispatcher(store, vault)
	if err := webhookDispatcher.Start(); err != nil {
		log.Printf("Warning: Outbound webhook dispatcher failed to start: %v", err)
	}
	notifier = notifications.NewMultiNotifier(notifier, webhookDispatcher)

	// ── Security ──────────────────────────────────────────────────────
	securityDefense := security.NewDefense(store, notifier)
	kyvik.SetSecurity(securityDefense)

	// ── Circuit Breaker ───────────────────────────────────────────────
	breakerManager := circuitbreaker.NewManager(func(trip circuitbreaker.TripResult) {
		_ = auditLogger.Log(context.Background(), types.AuditEntry{
			AgentID:   trip.AgentID,
			EventType: types.EventAgentLifecycle,
			Action:    "circuit_breaker_tripped",
			Details:   fmt.Sprintf("trigger=%s: %s", trip.Trigger, trip.Description),
			Timestamp: trip.Timestamp,
		})
		if notifier != nil {
			_ = notifier.Send(context.Background(), notifications.Event{
				Type:      "circuit_breaker",
				Severity:  "critical",
				Agent:     trip.AgentID,
				Title:     "Circuit breaker tripped",
				Detail:    fmt.Sprintf("[%s] %s", trip.Trigger, trip.Description),
				Timestamp: trip.Timestamp,
			})
		}
	})
	kyvik.SetCircuitBreakerManager(breakerManager)

	// Load circuit breaker system defaults.
	{
		const cbDefaultsKey = "circuit_breaker_defaults"
		raw, err := store.GetSystemState(context.Background(), cbDefaultsKey)
		var sysDefaults types.CircuitBreakerConfig
		if err == nil && raw != "" {
			_ = json.Unmarshal([]byte(raw), &sysDefaults)
		} else {
			yamlCB := cfg.CircuitBreaker
			sysDefaults = types.CircuitBreakerConfig{
				Enabled:               true,
				ErrorThreshold:        yamlCB.ErrorThreshold,
				ErrorWindowMinutes:    yamlCB.ErrorWindowMinutes,
				SpendingVelocityPct:   yamlCB.SpendingVelocityPct,
				SpendingWindowMinutes: yamlCB.SpendingWindowMinutes,
				ActionRatePerMinute:   yamlCB.ActionRatePerMinute,
				DestructiveLimit:      yamlCB.DestructiveLimit,
				LoopIdenticalCount:    yamlCB.LoopIdenticalCount,
			}
			if b, err := json.Marshal(sysDefaults); err == nil {
				_ = store.SetSystemState(context.Background(), cbDefaultsKey, string(b))
			}
		}
		breakerManager.SetSystemDefaults(sysDefaults)
	}

	pairedOrch.SetCircuitBreakerStatusProvider(func(agentID string) bool {
		return breakerManager.Status(agentID).Tripped
	})

	// ── Scheduler ─────────────────────────────────────────────────────
	var sched *scheduler.Scheduler
	if cfg.Scheduler.Enabled != nil && *cfg.Scheduler.Enabled {
		var schedErr error
		sched, schedErr = scheduler.New(store, msgQueue, scheduler.Config{
			Enabled:         true,
			DefaultTimezone: cfg.Scheduler.DefaultTimezone,
		})
		if schedErr != nil {
			log.Printf("Warning: Scheduler failed to initialize: %v", schedErr)
		} else {
			kyvik.SetScheduler(sched)
			if err := ktpRegistry.Register(schedulertool.New(sched)); err != nil {
				log.Printf("Warning: Failed to register scheduler tool: %v", err)
			}
		}
	}

	// ── Workflow ──────────────────────────────────────────────────────
	wfEngine := workflowengine.New(ktpExecutor, store)
	wfTool := workflowtool.New(wfEngine, store)
	if err := ktpRegistry.Register(wfTool); err != nil {
		log.Printf("Warning: Failed to register workflow tool: %v", err)
	}

	// ── Wire Notifier into Subsystems ─────────────────────────────────
	kyvik.SetNotifier(notifier)
	spendingTracker.SetNotifier(notifier, cfg.Notifications.Events.SpendingThreshold)
	if km != nil {
		km.SetNotifier(notifier)
	}
	chMgr.SetNotifier(notifier)

	// Fire notifications for channels that failed at startup.
	for _, ch := range chMgr.ListChannels() {
		if ch.Enabled && !ch.Connected && ch.Error != "" {
			_ = notifier.Send(context.Background(), notifications.Event{
				Type:      "channel_failure",
				Severity:  "warning",
				Title:     fmt.Sprintf("%s channel failed to connect at startup", ch.DisplayName),
				Detail:    ch.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// ── Router & Compression ──────────────────────────────────────────
	providerRegistry := router.NewProviderRegistry(kyvik.Models)
	unifiedRouter := router.NewRouter(providerRegistry)
	kyvik.SetRouter(unifiedRouter)

	compressor := compression.New(historyStore, memoryStore, spendingTracker, auditLogger,
		func(compressionModel string, agentCfg types.AgentConfig) models.Provider {
			p, _ := providerRegistry.GetProvider(agentCfg.ModelConfig.Provider)
			return p
		})
	kyvik.SetCompressor(compressor)
	assembler.SetCompressor(compressor)

	// ── Feedback & Workers ────────────────────────────────────────────
	kyvik.SetFeedbackRunner(feedback.New())
	kyvik.SetWorkspaceRoot(cfg.Sandbox.WorkspaceRoot)

	workerManager := workers.NewWorkerManager(providerRegistry, spendingTracker, memoryStore)
	workerManager.Start(context.Background())
	kyvik.SetWorkerManager(workerManager)

	// ── Cluster ───────────────────────────────────────────────────────
	var clusterMgr cluster.Manager
	if cfg.Cluster.Enabled != nil && *cfg.Cluster.Enabled {
		if cfg.Storage.Driver != "postgres" {
			return nil, fmt.Errorf("clustering requires PostgreSQL (storage.driver: postgres)")
		}
		var err error
		clusterMgr, err = cluster.NewClusterManager(
			cfg.Cluster,
			store,
			db,
			cfg.Storage.Postgres.DSN,
			cfg.Server.DataDir,
			version,
		)
		if err != nil {
			return nil, fmt.Errorf("cluster init: %w", err)
		}
	} else {
		clusterMgr = cluster.NewNoopManager()
	}
	kyvik.SetCluster(clusterMgr)

	// ── Retention ─────────────────────────────────────────────────────
	cfg.Retention.WorkspaceRoot = cfg.Sandbox.WorkspaceRoot
	pruner := retention.New(db, store, cfg.Retention)
	pruner.SetNotifier(notifier)
	pruner.SetQueue(msgQueue)
	kyvik.SetPruner(pruner)
	pruner.Start()

	log.Printf("Warning: built-in backup manager is not yet implemented for PostgreSQL")

	// ── Auth Provider ─────────────────────────────────────────────────
	authProvider, authErr := buildAuthProvider(cfg, store)
	if authErr != nil {
		return nil, fmt.Errorf("build auth provider: %w", authErr)
	}

	// ── Templates ─────────────────────────────────────────────────────
	templateSvc := templates.New(store)
	kyvik.SetTemplateService(templateSvc)

	startCtx := context.Background()
	if seeded, err := templatebuiltins.EnsureBuiltinTemplates(startCtx, templateSvc, store); err != nil {
		log.Printf("Warning: built-in template seeding failed: %v", err)
	} else if seeded > 0 {
		log.Printf("Seeded %d built-in agent templates", seeded)
	}

	// ── Integrations ──────────────────────────────────────────────────
	var integrationMgr *integrations.Manager
	localIntegrationDir := filepath.Join(cfg.Server.DataDir, "integrations", "local")
	if integrationLoader, err := integrations.NewLoader(integrations.BuiltinTemplates, localIntegrationDir); err != nil {
		log.Printf("Warning: Integration loader failed: %v", err)
	} else if mgr, err := integrations.NewManager(integrationLoader, store, vault); err != nil {
		log.Printf("Warning: Integration manager failed: %v", err)
	} else {
		integrationMgr = mgr
		if err := integrationMgr.Refresh(); err != nil {
			log.Printf("Warning: Integration catalog load failed: %v", err)
		} else {
			log.Printf("Integration catalog: %d templates loaded", len(integrationMgr.Available()))
		}
		kyvik.SetIntegrationManager(integrationMgr)
		assembler.SetIntegrationPromptProvider(integrationMgr)
	}

	// ── Web Handler ───────────────────────────────────────────────────
	apiKeySvc := apikeys.New(store)

	exportDeps := backup.ExportDeps{
		Store:         store,
		Vault:         vault,
		MemoryStore:   memoryStore,
		HistoryStore:  historyStore,
		ConvStore:     historyStore,
		ScheduleStore: store,
	}
	webhookHandler := webhooks.New(store, vault, kyvik, auditLogger)
	webOpts := []web.Option{
		web.WithWebUI(webuiAdapter), web.WithSecretStore(vault), web.WithBackupDeps(exportDeps),
		web.WithTimezone(cfg.Scheduler.DefaultTimezone), web.WithAuthProvider(authProvider),
		web.WithAPIKeys(apiKeySvc), web.WithTemplateService(templateSvc),
		web.WithAuditStreamConfig(cfg.AuditStream),
		web.WithWebhookHandler(webhookHandler),
		web.WithOutboundWebhookStore(store),
	}
	if integrationMgr != nil {
		webOpts = append(webOpts, web.WithIntegrationManager(integrationMgr))
	}
	webOpts = append(webOpts, web.WithProviderManager(providerMgr))
	webOpts = append(webOpts, web.WithChannelManager(chMgr))
	webOpts = append(webOpts, web.WithDiscordAuthStore(store))
	webOpts = append(webOpts, web.WithWorkflowEngine(wfEngine))
	webOpts = append(webOpts, web.WithClusterManager(clusterMgr))
	webOpts = append(webOpts, web.WithObsidianVaultManager(obsidianMgr))
	webOpts = append(webOpts, web.WithTrustedProxies(cfg.Server.TrustedProxies))
	if cfg.Auth.Type == "delegated" {
		webOpts = append(webOpts, web.WithManagedAPI(store, cfg.Auth.Delegated.SharedSecret, version))
	}
	handler := web.SetupRoutes(kyvik, webOpts...)

	// ── Post-Start Hooks ──────────────────────────────────────────────
	// These run after kyvik.Start() in main.go.
	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Load channel adapters now that the message router is running.
		if err := chMgr.LoadChannels(ctx); err != nil {
			log.Printf("Warning: channel manager load failed: %v", err)
		}
		for _, name := range kyvik.ListChannelNames() {
			if name == "slack" || name == "discord" {
				channels = append(channels, name)
			}
		}
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Patch tool grants on existing guide agent.
		guide.PatchExistingGuide(ctx, guide.ProvisionDeps{
			Store:        store,
			SkillManager: kyvik.SkillManager(),
			GuideConfig:  cfg.Guide,
		})
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Reconcile agent states.
		if err := kyvik.Reconcile(ctx); err != nil {
			log.Printf("Warning: Reconciliation failed: %v", err)
		}
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Send guide welcome message on first run.
		if cfg.Guide.Enabled != nil && *cfg.Guide.Enabled {
			if err := guide.SendWelcomeMessage(ctx, store, kyvik, len(providerNames) > 0); err != nil {
				log.Printf("Warning: guide welcome message failed: %v", err)
			}
		}
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Start background memory embedder.
		if ep := findEmbeddingProvider(kyvik); ep != nil {
			embedder := memory.NewEmbedder(memoryStore, ep)
			embedder.Start(ctx)
			res.Closers = append(res.Closers, func() { embedder.Stop() })
			log.Println("Memory embedder started")
		}
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Start background memory archiver.
		archiver := memory.NewArchiver(memoryStore, store, cfg.Memory.DecayDays, cfg.Memory.ArchivalTime)
		archiver.Start(ctx)
		res.Closers = append(res.Closers, func() { archiver.Stop() })
		log.Printf("Memory archiver started (decay: %d days, run: %s)", cfg.Memory.DecayDays, cfg.Memory.ArchivalTime)
	})

	res.PostStart = append(res.PostStart, func(ctx context.Context) {
		// Migrate agents with empty ToolGrants to tier defaults.
		agents, err := store.ListAgents(ctx)
		if err != nil {
			log.Printf("Warning: could not list agents for tool grant migration: %v", err)
			return
		}
		migrated := 0
		for _, agent := range agents {
			if agent.IsGuide || len(agent.ToolGrants) > 0 {
				continue
			}
			agentTier := ktp.ResolveAgentTier(agent.Template)
			defaults := ktpRegistry.DefaultToolsForTier(agentTier)
			if len(defaults) == 0 {
				continue
			}
			agent.ToolGrants = defaults
			agent.UpdatedAt = time.Now().UTC()
			if err := store.UpdateAgent(ctx, agent); err != nil {
				log.Printf("Warning: could not migrate tool grants for %s: %v", agent.ID, err)
			} else {
				migrated++
			}
		}
		if migrated > 0 {
			fmt.Printf("  ToolGrants: migrated %d agents to tier defaults\n", migrated)
		}
	})

	// ── Team Count ────────────────────────────────────────────────────
	var teamCount int
	if configuredTeams, err := store.ListTeams(context.Background()); err == nil {
		teamCount = len(configuredTeams)
	}

	// ── Populate Result ───────────────────────────────────────────────
	res.Kyvik = kyvik
	res.Handler = handler
	res.ClusterManager = clusterMgr
	res.Scheduler = sched
	res.Queue = msgQueue
	res.ChannelManager = chMgr
	res.ProviderNames = providerNames
	res.Channels = channels
	res.KTPRegistry = ktpRegistry
	res.SandboxMgr = sandboxMgr
	res.AllowUnrestricted = allowUnrestricted
	res.NotifierType = notifierType
	res.SkillCount = skillCount
	res.IntegrationMgr = integrationMgr
	res.ObsidianMgr = obsidianMgr
	res.TeamCount = teamCount

	return res, nil
}

// buildAuthProvider creates the appropriate auth provider based on config.
func buildAuthProvider(cfg *config.Config, store users.Store) (authprovider.AuthProvider, error) {
	switch cfg.Auth.Type {
	case "local":
		const bootstrapCredsPath = "/etc/kyvik/initial-credentials"
		svc := users.New(store, users.AuthConfig{
			SessionTTL:         24 * time.Hour,
			MaxSessionsPerUser: 3,
			BootstrapCredsPath: bootstrapCredsPath,
		})
		createdAdmin, generatedPwd, bootstrapErr := svc.BootstrapAdminIfEmpty(
			context.Background(), cfg.Auth.Username, cfg.Auth.Password,
		)
		if bootstrapErr != nil {
			log.Printf("Warning: user bootstrap failed: %v", bootstrapErr)
		} else if createdAdmin && generatedPwd != "" {
			log.Printf("Initial admin account created. Credentials written to %s.", bootstrapCredsPath)
		}
		return local.New(svc), nil
	case "delegated":
		return delegated.NewFromConfig(store, cfg.Auth.Delegated, 24*time.Hour)
	case "oidc":
		return oidc.NewFromConfig(context.Background(), store, cfg.Auth.OIDC, 24*time.Hour)
	default:
		return nil, fmt.Errorf("auth.type %q not supported (valid: local, delegated, oidc)", cfg.Auth.Type)
	}
}

// findEmbeddingProvider returns the first registered provider that supports embeddings.
func findEmbeddingProvider(k *core.Kyvik) models.EmbeddingProvider {
	for _, name := range []string{"openai", "ollama"} {
		if ep := k.EmbeddingProvider(name); ep != nil {
			return ep
		}
	}
	return nil
}
