package tools

import (
	"context"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/tools/code"
	"github.com/kkjorsvik/kyvik/internal/tools/dbtool"
	"github.com/kkjorsvik/kyvik/internal/tools/file"
	dockertool "github.com/kkjorsvik/kyvik/internal/tools/docker"
	gittool "github.com/kkjorsvik/kyvik/internal/tools/git"
	"github.com/kkjorsvik/kyvik/internal/tools/github"
	"github.com/kkjorsvik/kyvik/internal/tools/httptool"
	memorytool "github.com/kkjorsvik/kyvik/internal/tools/memory"
	"github.com/kkjorsvik/kyvik/internal/tools/myspending"
	obsidiantool "github.com/kkjorsvik/kyvik/internal/tools/obsidian"
	"github.com/kkjorsvik/kyvik/internal/tools/restapi"
	"github.com/kkjorsvik/kyvik/internal/tools/sysinfo"
	"github.com/kkjorsvik/kyvik/internal/tools/shell"
	"github.com/kkjorsvik/kyvik/internal/tools/teamtool"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// WorkspaceFunc resolves an agent's workspace directory.
// Matches the signature of file.WorkspaceFunc, shell.WorkspaceFunc, code.WorkspaceFunc.
type WorkspaceFunc func(agentID string) (string, error)

// SandboxSecretResolver resolves a secret by key. Used in the sandbox binary
// to resolve secrets via Unix socket instead of env vars.
type SandboxSecretResolver func(key string) (string, error)

// RegistrationOptions configures which built-in tools to register.
type RegistrationOptions struct {
	WorkspaceFunc          WorkspaceFunc
	MemoryStore            memory.MemoryStore                                               // nil = skip memory tool
	AllowedHostsFunc       func(agentID string) ([]string, error)                           // nil = skip http tool
	AllowedCommandsFunc    func(agentID string) ([]string, error)                           // nil = skip shell tool
	AgentTierFunc          func(agentID string) (string, error)                             // needed by shell, file, http tools
	HostPathsFunc          func(agentID string) (*file.HostPathConfig, error)               // needed by file tool for power tier
	TeamManager            teamtool.TeamManager                                             // nil = skip team tools
	InternalBus            teamtool.MessageBus                                              // nil = skip team tools
	AgentLookup            func(ctx context.Context, id string) (*types.AgentConfig, error) // needed by team tools
	EndpointConfigsFunc    restapi.EndpointConfigsFunc                                      // nil = skip rest_api tool
	SecretResolverFunc     restapi.SecretResolverFunc                                       // nil = skip rest_api tool
	RESTAllowedHostsFunc   restapi.AllowedHostsFunc                                        // nil = no private IP exemptions for rest_api
	StatusStore            sysinfo.StatusStore                                              // nil = skip system_status tool
	SkillReadPaths         []string                                                         // skill-level read path restrictions
	SkillWritePaths        []string                                                         // skill-level write path restrictions
	SandboxSecretResolver      SandboxSecretResolver                                        // nil = fall back to env var lookup
	DatabaseConnectionsFunc    dbtool.ConnectionConfigsFunc                                 // nil = skip database tool
	DatabaseSecretResolverFunc dbtool.SecretResolverFunc                                   // nil = skip database tool
	ObsidianVaultPathFunc      func(ctx context.Context, name string) (string, error)      // nil = skip obsidian tool
	ObsidianVaultAccessFunc    func(agentID string) ([]string, error)                      // nil = no vault access checks
}

// RegisterBuiltinTools registers the core KTP tools (file, memory, http, shell, code).
// Memory is skipped if opts.MemoryStore is nil (e.g. in sandbox binary).
// Shell requires AllowedCommandsFunc, AgentTierFunc, and WorkspaceFunc.
// Code requires WorkspaceFunc.
func RegisterBuiltinTools(registry *ktp.Registry, opts RegistrationOptions) error {
	// File tool.
	if opts.WorkspaceFunc != nil {
		var fileOpts []file.Option
		if opts.AgentTierFunc != nil {
			fileOpts = append(fileOpts, file.WithTierFunc(file.TierFunc(opts.AgentTierFunc)))
		}
		if opts.HostPathsFunc != nil {
			fileOpts = append(fileOpts, file.WithHostPathsFunc(file.HostPathsFunc(opts.HostPathsFunc)))
		}
		if len(opts.SkillReadPaths) > 0 || len(opts.SkillWritePaths) > 0 {
			fileOpts = append(fileOpts, file.WithSkillPaths(opts.SkillReadPaths, opts.SkillWritePaths))
		}
		if err := registry.Register(file.New(file.WorkspaceFunc(opts.WorkspaceFunc), fileOpts...)); err != nil {
			return err
		}
	}

	// Memory tool (only in-process, not in sandbox).
	if opts.MemoryStore != nil {
		if err := registry.Register(memorytool.New(opts.MemoryStore)); err != nil {
			return err
		}
	}

	// HTTP tool.
	if opts.AllowedHostsFunc != nil {
		var httpOpts []httptool.HTTPOption
		if opts.AgentTierFunc != nil {
			httpOpts = append(httpOpts, httptool.WithTierFunc(httptool.TierFunc(opts.AgentTierFunc)))
		}
		if err := registry.Register(httptool.New(httptool.AllowedHostsFunc(opts.AllowedHostsFunc), httpOpts...)); err != nil {
			return err
		}
	}

	// Shell tool.
	if opts.AllowedCommandsFunc != nil && opts.AgentTierFunc != nil && opts.WorkspaceFunc != nil {
		if err := registry.Register(shell.New(
			shell.AllowedCommandsFunc(opts.AllowedCommandsFunc),
			shell.WorkspaceFunc(opts.WorkspaceFunc),
			shell.AgentTierFunc(opts.AgentTierFunc),
		)); err != nil {
			return err
		}
	}

	// Code tool.
	if opts.WorkspaceFunc != nil {
		if err := registry.Register(code.New(code.WorkspaceFunc(opts.WorkspaceFunc))); err != nil {
			return err
		}
	}

	// Team tools.
	if opts.TeamManager != nil && opts.InternalBus != nil && opts.AgentLookup != nil {
		if err := registry.Register(teamtool.NewDelegateTool(opts.TeamManager, opts.InternalBus, opts.AgentLookup)); err != nil {
			return err
		}
		if err := registry.Register(teamtool.NewBroadcastTool(opts.TeamManager, opts.InternalBus)); err != nil {
			return err
		}
		if err := registry.Register(teamtool.NewStatusTool(opts.TeamManager, opts.AgentLookup)); err != nil {
			return err
		}
		if err := registry.Register(teamtool.NewRecallTool(opts.TeamManager, opts.InternalBus, opts.AgentLookup)); err != nil {
			return err
		}
	}

	// GitHub tool (dedicated integration with vault-backed auth).
	var githubOpts []github.Option
	if opts.SandboxSecretResolver != nil {
		githubOpts = append(githubOpts, github.WithSecretResolver(github.SecretResolver(opts.SandboxSecretResolver)))
	}
	if err := registry.Register(github.New(githubOpts...)); err != nil {
		return err
	}

	// Git tool (local git operations, needs workspace).
	if opts.WorkspaceFunc != nil {
		var gitOpts []gittool.Option
		if opts.SandboxSecretResolver != nil {
			gitOpts = append(gitOpts, gittool.WithSecretResolver(gittool.SecretResolver(opts.SandboxSecretResolver)))
		}
		if err := registry.Register(gittool.New(gittool.WorkspaceFunc(opts.WorkspaceFunc), gitOpts...)); err != nil {
			return err
		}
	}

	// Docker tool (container management, needs workspace).
	if opts.WorkspaceFunc != nil {
		var dockerOpts []dockertool.Option
		if opts.SandboxSecretResolver != nil {
			dockerOpts = append(dockerOpts, dockertool.WithSecretResolver(dockertool.SecretResolver(opts.SandboxSecretResolver)))
		}
		if err := registry.Register(dockertool.New(dockertool.WorkspaceFunc(opts.WorkspaceFunc), dockerOpts...)); err != nil {
			return err
		}
	}

	// System status tool (inline, for guide agent).
	if opts.StatusStore != nil {
		if err := registry.Register(sysinfo.New(opts.StatusStore)); err != nil {
			return err
		}
	}

	// My-spending tool (inline, self-scoped spending queries for all agents).
	if opts.StatusStore != nil {
		if err := registry.Register(myspending.New(opts.StatusStore)); err != nil {
			return err
		}
	}

	// REST API tool (pre-configured endpoints with vault-backed auth).
	if opts.EndpointConfigsFunc != nil && opts.SecretResolverFunc != nil {
		var restapiOpts []restapi.Option
		if opts.AgentTierFunc != nil {
			restapiOpts = append(restapiOpts, restapi.WithTierFunc(restapi.TierFunc(opts.AgentTierFunc)))
		}
		if opts.RESTAllowedHostsFunc != nil {
			restapiOpts = append(restapiOpts, restapi.WithAllowedHostsFunc(opts.RESTAllowedHostsFunc))
		}
		if err := registry.Register(restapi.New(opts.EndpointConfigsFunc, opts.SecretResolverFunc, restapiOpts...)); err != nil {
			return err
		}
	}

	// Database tool (pre-configured connections with vault-backed auth).
	if opts.DatabaseConnectionsFunc != nil && opts.DatabaseSecretResolverFunc != nil {
		if err := registry.Register(dbtool.New(opts.DatabaseConnectionsFunc, opts.DatabaseSecretResolverFunc)); err != nil {
			return err
		}
	}

	// Obsidian tool (vault-based note management).
	if opts.ObsidianVaultPathFunc != nil {
		obsOpts := []obsidiantool.Option{
			obsidiantool.WithVaultPath(obsidiantool.VaultPathFunc(opts.ObsidianVaultPathFunc)),
		}
		if opts.ObsidianVaultAccessFunc != nil {
			obsOpts = append(obsOpts, obsidiantool.WithVaultAccess(obsidiantool.VaultAccessFunc(opts.ObsidianVaultAccessFunc)))
		}
		if err := registry.Register(obsidiantool.New(obsOpts...)); err != nil {
			return err
		}
	}

	return nil
}
