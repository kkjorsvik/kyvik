package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kkjorsvik/kyvik/internal/bootstrap"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

var (
	version = "2026.02.22"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	startedAt := time.Now()
	logStage := func(stage string) {
		log.Printf("Startup: %s (%s)", stage, time.Since(startedAt).Round(time.Millisecond))
	}

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("Kyvik v%s (%s, %s)\n", version, commit, date)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrateCommand(os.Args[2:]); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		return
	}

	// Parse flags.
	configPath := flag.String("config", "", "path to kyvik.yaml config file")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Kyvik v%s (%s, %s)\n", version, commit, date)
		return
	}

	// Load configuration.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = "kyvik.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		// Try fallback to example config.
		cfg, err = config.Load("configs/kyvik.example.yaml")
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		log.Println("Using example config (configs/kyvik.example.yaml)")
	}
	logStage("config loaded")

	// Build all subsystems.
	res, err := bootstrap.Build(cfg, version, logStage)
	if err != nil {
		log.Fatalf("Bootstrap failed: %v", err)
	}

	// Ensure deferred closers run on exit.
	for _, closer := range res.Closers {
		defer closer()
	}

	// Start the cluster manager.
	startCtx := context.Background()
	if err := res.ClusterManager.Start(startCtx); err != nil {
		log.Fatalf("cluster start: %v", err)
	}

	// Start the core runtime (message router, etc.).
	if err := res.Kyvik.Start(startCtx); err != nil {
		log.Fatalf("Failed to start kyvik runtime: %v", err)
	}
	logStage("core runtime started")

	// Run post-start hooks (channel loading, reconcile, guide, embedder, etc.).
	for _, fn := range res.PostStart {
		fn(startCtx)
	}

	// Start scheduler if enabled.
	if res.Scheduler != nil {
		if err := res.Scheduler.Start(startCtx); err != nil {
			log.Printf("Warning: Scheduler start failed: %v", err)
		}
	}

	// Replay any pending messages from a previous run.
	if err := res.Queue.Replay(context.Background()); err != nil {
		log.Printf("Warning: Queue replay failed: %v", err)
	}
	logStage("queue replay complete")

	// Create and start HTTP server.
	server := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: res.Handler,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()
	logStage("http server listening")

	// ── Startup Banner ────────────────────────────────────────────────
	printStartupBanner(cfg, res)
	logStage("startup complete")

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	if err := res.Kyvik.Shutdown(shutdownCtx); err != nil {
		log.Printf("Kyvik shutdown error: %v", err)
	}
	res.ObsidianMgr.Stop()
	if err := res.ClusterManager.Stop(); err != nil {
		log.Printf("Cluster shutdown error: %v", err)
	}

	if res.SeparateAuditCloser != nil {
		if err := res.SeparateAuditCloser.Close(); err != nil {
			log.Printf("Audit database close error: %v", err)
		}
	}

	log.Println("Shutdown complete")
}

func printStartupBanner(cfg *config.Config, res *bootstrap.Result) {
	providerStr := "(none)"
	if len(res.ProviderNames) > 0 {
		providerStr = strings.Join(res.ProviderNames, ", ")
	}
	channelStr := "(none)"
	if len(res.Channels) > 0 {
		channelStr = strings.Join(res.Channels, ", ")
	}

	fmt.Printf("Kyvik v%s (%s)\n", version, commit[:min(7, len(commit))])
	fmt.Printf("  Listen:    %s\n", cfg.Server.ListenAddr)
	switch cfg.Storage.Driver {
	case "postgres":
		dbLabel := "postgres"
		if u, err := url.Parse(cfg.Storage.Postgres.DSN); err == nil && u.Host != "" {
			dbName := strings.TrimPrefix(u.Path, "/")
			if dbName != "" {
				dbLabel = fmt.Sprintf("postgres (%s/%s)", u.Host, dbName)
			} else {
				dbLabel = fmt.Sprintf("postgres (%s)", u.Host)
			}
		}
		fmt.Printf("  Database:  %s\n", dbLabel)
	default:
		fmt.Printf("  Database:  unsupported driver %q\n", cfg.Storage.Driver)
	}
	fmt.Printf("  Providers: %s\n", providerStr)
	fmt.Printf("  Channels:  %s\n", channelStr)
	if primaryConnected, dedicatedCount := res.Kyvik.SlackStatus(); primaryConnected {
		fmt.Printf("  Slack:     primary + %d dedicated apps\n", dedicatedCount)
	}
	fmt.Printf("  Queue:     depth=%d, behavior=%s\n", cfg.Queue.Depth, cfg.Queue.FullBehavior)
	fmt.Printf("  Secrets:   enabled (AES-256-GCM)\n")
	fmt.Printf("  KTP:       %d tools registered\n", len(res.KTPRegistry.List()))

	for _, tier := range []string{ktp.TierReader, ktp.TierGuide, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin} {
		defs := res.KTPRegistry.GetToolDefinitionsForModel("_startup_check", tier, nil)
		fmt.Printf("  KTP tier %-8s: %d tool actions visible\n", tier, len(defs))
	}

	if res.SandboxMgr != nil {
		fmt.Printf("  Sandbox:   enabled (runner: %s)\n", res.SandboxMgr.RunnerPath())
	} else {
		fmt.Printf("  Sandbox:   disabled (runner not found)\n")
	}
	hostAccessStr := cfg.Sandbox.HostAccess
	if res.AllowUnrestricted {
		hostAccessStr += " (unrestricted tier: enabled)"
	} else {
		hostAccessStr += " (unrestricted tier: disabled)"
	}
	fmt.Printf("  HostAccess: %s\n", hostAccessStr)
	if len(cfg.Sandbox.ExtraPaths.ReadWrite) > 0 || len(cfg.Sandbox.ExtraPaths.ReadOnly) > 0 {
		fmt.Printf("  ExtraPaths: rw=%d, ro=%d\n", len(cfg.Sandbox.ExtraPaths.ReadWrite), len(cfg.Sandbox.ExtraPaths.ReadOnly))
	}
	fmt.Printf("  Notify:    %s\n", res.NotifierType)
	schedulerStatus := "disabled"
	if res.Scheduler != nil {
		schedulerStatus = fmt.Sprintf("enabled (tz: %s)", cfg.Scheduler.DefaultTimezone)
	}
	fmt.Printf("  Scheduler: %s\n", schedulerStatus)
	retentionStatus := "disabled"
	if cfg.Retention.Enabled != nil && *cfg.Retention.Enabled {
		retentionStatus = fmt.Sprintf("enabled (schedule: %s, audit: %dd, history: %dd)",
			cfg.Retention.Schedule, cfg.Retention.AuditLogsDays, cfg.Retention.ConversationHistoryDays)
	}
	fmt.Printf("  Retention: %s\n", retentionStatus)
	backupStatus := "disabled"
	if cfg.Backup.Enabled != nil && *cfg.Backup.Enabled {
		backupStatus = fmt.Sprintf("enabled (schedule: %s, keep: %d)", cfg.Backup.Schedule, cfg.Backup.Retention)
	}
	fmt.Printf("  Backup:    %s\n", backupStatus)
	fmt.Printf("  Teams:     %d configured\n", res.TeamCount)
	fmt.Printf("  Breaker:   enabled (errors=%d/%dm, rate=%d/min, destructive=%d)\n",
		types.DefaultCircuitBreakerConfig().ErrorThreshold,
		types.DefaultCircuitBreakerConfig().ErrorWindowMinutes,
		types.DefaultCircuitBreakerConfig().ActionRatePerMinute,
		types.DefaultCircuitBreakerConfig().DestructiveLimit)
	if res.SkillCount > 0 {
		fmt.Printf("  Skills:    %d loaded\n", res.SkillCount)
	} else {
		fmt.Printf("  Skills:    disabled\n")
	}
	if res.IntegrationMgr != nil {
		fmt.Printf("  Integrations: %d templates loaded\n", len(res.IntegrationMgr.Available()))
	} else {
		fmt.Printf("  Integrations: disabled\n")
	}
	fmt.Printf("  API:       enabled (/api/v1/)\n")
	switch cfg.Auth.Type {
	case "local":
		fmt.Printf("  Auth:      local (user: %s)\n", cfg.Auth.Username)
	case "delegated":
		fmt.Printf("  Auth:      delegated (sett: %s)\n", cfg.Auth.Delegated.SettURL)
	case "oidc":
		fmt.Printf("  Auth:      oidc (issuer: %s)\n", cfg.Auth.OIDC.IssuerURL)
	}
}
