// Package docker implements a KTP tool for managing Docker containers
// within an agent's workspace sandbox.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/tools/executil"
)

const (
	localTimeout   = 30 * time.Second
	networkTimeout = 120 * time.Second
	buildTimeout   = 300 * time.Second
	runTimeout     = 120 * time.Second
)

// WorkspaceFunc resolves an agent's workspace directory.
type WorkspaceFunc func(agentID string) (string, error)

// SecretResolver resolves a secret by key.
type SecretResolver func(key string) (string, error)

// Tool implements Docker container operations.
type Tool struct {
	workspace      WorkspaceFunc
	secretResolver SecretResolver
}

// Option configures Tool.
type Option func(*Tool)

// WithSecretResolver sets a custom secret resolver.
func WithSecretResolver(fn SecretResolver) Option {
	return func(t *Tool) { t.secretResolver = fn }
}

// New creates a Docker tool.
func New(workspace WorkspaceFunc, opts ...Option) *Tool {
	t := &Tool{workspace: workspace}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Declaration returns the tool schema.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:            "docker",
		Version:         "1.0.0",
		Description:     "Docker container management",
		MinTier:         ktp.TierOperator,
		RequiredSecrets: []string{"docker:registry_token"},
		Capabilities: []ktp.Capability{
			{Type: "docker", Access: "read", Resource: "*"},
			{Type: "docker", Access: "manage", Resource: "*"},
			{Type: "docker", Access: "registry", Resource: "*"},
		},
		Actions: []ktp.ActionSpec{
			// Manage actions
			{
				Name:        "build",
				Description: "Build a Docker image from a Dockerfile",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"tag":          {Type: "string"},
						"context_path": {Type: "string", Description: "Build context directory (default: .)"},
						"dockerfile":   {Type: "string", Description: "Dockerfile path relative to context"},
						"build_args":   {Type: "object", Description: "Build arguments as key-value pairs"},
						"no_cache":     {Type: "boolean", Description: "Disable build cache"},
					},
					Required: []string{"tag"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image_id": {Type: "string"},
						"tag":      {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "manage", Resource: "*"}},
			},
			{
				Name:        "run",
				Description: "Run a command in a new container",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image":   {Type: "string"},
						"name":    {Type: "string", Description: "Container name"},
						"env":     {Type: "object", Description: "Environment variables"},
						"ports":   {Type: "object", Description: "Port mappings (host:container)"},
						"volumes": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Volume mounts (src:dest)"},
						"command": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Command to run"},
						"detach":  {Type: "boolean", Description: "Run in background (default: false)"},
						"workdir": {Type: "string", Description: "Working directory inside container"},
					},
					Required: []string{"image"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"container_id": {Type: "string"},
						"stdout":       {Type: "string"},
						"stderr":       {Type: "string"},
						"exit_code":    {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "manage", Resource: "*"}},
			},
			{
				Name:        "stop",
				Description: "Stop a running container",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"container": {Type: "string"},
						"timeout":   {Type: "integer", Description: "Seconds to wait before killing (default: 10)"},
					},
					Required: []string{"container"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"stopped": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "manage", Resource: "*"}},
			},
			{
				Name:        "rm",
				Description: "Remove a container",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"container": {Type: "string"},
						"force":     {Type: "boolean", Description: "Force remove running container"},
					},
					Required: []string{"container"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"removed": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "manage", Resource: "*"}},
			},
			{
				Name:        "logs",
				Description: "Fetch container logs",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"container": {Type: "string"},
						"tail":      {Type: "integer", Description: "Number of lines from end (default: 100)"},
					},
					Required: []string{"container"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"stdout": {Type: "string"},
						"stderr": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "read", Resource: "*"}},
			},
			// Read actions
			{
				Name:        "ps",
				Description: "List containers",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"all": {Type: "boolean", Description: "Include stopped containers"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"containers": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "read", Resource: "*"}},
			},
			{
				Name:        "images",
				Description: "List images",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"images": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "read", Resource: "*"}},
			},
			{
				Name:        "inspect",
				Description: "Return detailed information on a container or image",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"target": {Type: "string", Description: "Container or image ID/name"},
					},
					Required: []string{"target"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"info": {Type: "object"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "read", Resource: "*"}},
			},
			// Registry actions
			{
				Name:        "pull",
				Description: "Pull an image from a registry",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image": {Type: "string"},
					},
					Required: []string{"image"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image":  {Type: "string"},
						"digest": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "registry", Resource: "*"}},
			},
			{
				Name:        "push",
				Description: "Push an image to a registry",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image": {Type: "string"},
					},
					Required: []string{"image"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image":  {Type: "string"},
						"digest": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "docker", Access: "registry", Resource: "*"}},
			},
		},
	}
}

// Execute runs the requested docker action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	switch req.Action {
	case "build":
		return t.execBuild(ctx, req)
	case "run":
		return t.execRun(ctx, req)
	case "stop":
		return t.execStop(ctx, req)
	case "rm":
		return t.execRm(ctx, req)
	case "logs":
		return t.execLogs(ctx, req)
	case "ps":
		return t.execPs(ctx, req)
	case "images":
		return t.execImages(ctx, req)
	case "inspect":
		return t.execInspect(ctx, req)
	case "pull":
		return t.execPull(ctx, req)
	case "push":
		return t.execPush(ctx, req)
	}
	return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
}

// --- Read actions ---

func (t *Tool) execPs(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	args := []string{"ps", "--format", "{{json .}}"}
	if boolDefault(req.Parameters, "all", false) {
		args = append(args, "--all")
	}

	result, err := t.runDocker(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker ps failed: %s", result.Stderr)), nil
	}

	containers := parseJSONLines(result.Stdout)
	return okResp(req.ID, map[string]any{"containers": containers}), nil
}

func (t *Tool) execImages(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	result, err := t.runDocker(ctx, ws, []string{"images", "--format", "{{json .}}"}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker images failed: %s", result.Stderr)), nil
	}

	images := parseJSONLines(result.Stdout)
	return okResp(req.ID, map[string]any{"images": images}), nil
}

func (t *Tool) execInspect(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	target, err := strParam(req.Parameters, "target")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateContainerName(target); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	result, err := t.runDocker(ctx, ws, []string{"inspect", target}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker inspect failed: %s", result.Stderr)), nil
	}

	var inspectResult []map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &inspectResult); err != nil {
		return errResp(req.ID, fmt.Sprintf("parse inspect output: %v", err)), nil
	}
	if len(inspectResult) == 0 {
		return errResp(req.ID, "no results from inspect"), nil
	}

	return okResp(req.ID, map[string]any{"info": inspectResult[0]}), nil
}

// --- Manage actions ---

func (t *Tool) execRun(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	image, err := strParam(req.Parameters, "image")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	detach := boolDefault(req.Parameters, "detach", false)

	args := []string{"run"}

	// Auto-cleanup for attached mode only.
	if !detach {
		args = append(args, "--rm")
	}

	// Container naming.
	name := strDefault(req.Parameters, "name", "")
	if name != "" {
		args = append(args, "--name", fmt.Sprintf("kyvik-%s-%s", req.AgentID, name))
	}

	// Default resource limits.
	args = append(args,
		"--memory=512m",
		"--cpus=1.0",
		"--pids-limit=256",
	)

	// Default network isolation.
	args = append(args, "--network=none")

	// Default filesystem protection.
	args = append(args, "--read-only", "--tmpfs", "/tmp")

	// Environment variables.
	if envMap := mapParam(req.Parameters, "env"); len(envMap) > 0 {
		for k, v := range envMap {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Port mappings.
	if portMap := mapParam(req.Parameters, "ports"); len(portMap) > 0 {
		for host, container := range portMap {
			args = append(args, "-p", fmt.Sprintf("%s:%s", host, container))
		}
	}

	// Volume mounts.
	volumes := strSliceParam(req.Parameters, "volumes")
	if len(volumes) > 0 {
		if err := validateVolumeMounts(ws, volumes); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		for _, v := range volumes {
			parts := strings.SplitN(v, ":", 2)
			src := parts[0]
			dest := parts[1]
			absSrc, _ := executil.SafePath(ws, src)
			args = append(args, "-v", fmt.Sprintf("%s:%s", absSrc, dest))
		}
	}

	// Working directory.
	if workdir := strDefault(req.Parameters, "workdir", ""); workdir != "" {
		args = append(args, "-w", workdir)
	}

	// Detach mode.
	if detach {
		args = append(args, "-d")
	}

	// Image.
	args = append(args, image)

	// Command.
	cmd := strSliceParam(req.Parameters, "command")
	if len(cmd) > 0 {
		args = append(args, cmd...)
	}

	result, err := t.runDocker(ctx, ws, args, runTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}

	if detach {
		if result.ExitCode != 0 {
			return errResp(req.ID, fmt.Sprintf("docker run failed: %s", result.Stderr)), nil
		}
		containerID := strings.TrimSpace(result.Stdout)
		return okResp(req.ID, map[string]any{
			"container_id": containerID,
		}), nil
	}

	return okResp(req.ID, map[string]any{
		"stdout":    result.Stdout,
		"stderr":    result.Stderr,
		"exit_code": result.ExitCode,
	}), nil
}

func (t *Tool) execStop(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	container, err := strParam(req.Parameters, "container")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateContainerName(container); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	timeout := intDefault(req.Parameters, "timeout", 10)
	args := []string{"stop", "--time", strconv.Itoa(timeout), container}

	result, err := t.runDocker(ctx, ws, args, time.Duration(timeout+15)*time.Second)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker stop failed: %s", result.Stderr)), nil
	}

	return okResp(req.ID, map[string]any{"stopped": true}), nil
}

func (t *Tool) execRm(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	container, err := strParam(req.Parameters, "container")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateContainerName(container); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	args := []string{"rm"}
	if boolDefault(req.Parameters, "force", false) {
		args = append(args, "--force")
	}
	args = append(args, container)

	result, err := t.runDocker(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker rm failed: %s", result.Stderr)), nil
	}

	return okResp(req.ID, map[string]any{"removed": true}), nil
}

func (t *Tool) execLogs(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	container, err := strParam(req.Parameters, "container")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateContainerName(container); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	tail := intDefault(req.Parameters, "tail", 100)
	if tail < 1 {
		tail = 1
	}
	if tail > 10000 {
		tail = 10000
	}

	args := []string{"logs", "--tail", strconv.Itoa(tail), container}

	result, err := t.runDocker(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker logs failed: %s", result.Stderr)), nil
	}

	return okResp(req.ID, map[string]any{
		"stdout": result.Stdout,
		"stderr": result.Stderr,
	}), nil
}

func (t *Tool) execBuild(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	tag, err := strParam(req.Parameters, "tag")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	contextPath := strDefault(req.Parameters, "context_path", ".")
	if contextPath != "." {
		if _, err := executil.SafePath(ws, contextPath); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid context_path: %v", err)), nil
		}
	}

	args := []string{"build", "-t", tag}

	if boolDefault(req.Parameters, "no_cache", false) {
		args = append(args, "--no-cache")
	}

	if buildArgs := mapParam(req.Parameters, "build_args"); len(buildArgs) > 0 {
		for k, v := range buildArgs {
			args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
		}
	}

	if dockerfile := strDefault(req.Parameters, "dockerfile", ""); dockerfile != "" {
		if _, err := executil.SafePath(ws, dockerfile); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid dockerfile path: %v", err)), nil
		}
		args = append(args, "-f", dockerfile)
	}

	args = append(args, contextPath)

	result, err := t.runDocker(ctx, ws, args, buildTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker build failed: %s", result.Stderr)), nil
	}

	// Parse image ID from build output.
	imageID := parseImageID(result.Stdout)

	return okResp(req.ID, map[string]any{
		"image_id": imageID,
		"tag":      tag,
	}), nil
}

// --- Registry actions ---

func (t *Tool) execPull(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	image, err := strParam(req.Parameters, "image")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	result, err := t.runDocker(ctx, ws, []string{"pull", image}, networkTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker pull failed: %s", result.Stderr)), nil
	}

	digest := parseDigest(result.Stdout)

	return okResp(req.ID, map[string]any{
		"image":  image,
		"digest": digest,
	}), nil
}

func (t *Tool) execPush(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	image, err := strParam(req.Parameters, "image")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	result, err := t.runDocker(ctx, ws, []string{"push", image}, networkTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("docker error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("docker push failed: %s", result.Stderr)), nil
	}

	digest := parseDigest(result.Stdout)

	return okResp(req.ID, map[string]any{
		"image":  image,
		"digest": digest,
	}), nil
}

// --- Core execution ---

// runDocker executes a docker command with environment hardening.
func (t *Tool) runDocker(ctx context.Context, workDir string, args []string, timeout time.Duration) (*executil.ProcessResult, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}

	return executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    "docker",
		Args:       args,
		WorkingDir: workDir,
		Env:        os.Environ(),
		Timeout:    timeout,
	})
}

// --- Validation ---

// validateArgs checks for blocked docker flags and operations.
func validateArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no docker arguments provided")
	}

	// Block dangerous subcommands.
	sub := strings.ToLower(args[0])
	switch sub {
	case "system", "network", "volume":
		return fmt.Errorf("docker %s is not allowed", sub)
	case "exec":
		return fmt.Errorf("docker exec is not allowed")
	}

	for _, arg := range args {
		lower := strings.ToLower(arg)
		switch {
		case lower == "--privileged":
			return fmt.Errorf("--privileged is not allowed")
		case lower == "--pid=host" || (lower == "--pid" && hasNext(args, arg, "host")):
			return fmt.Errorf("--pid=host is not allowed")
		case lower == "--network=host" || (lower == "--network" && hasNext(args, arg, "host")):
			return fmt.Errorf("--network=host is not allowed")
		case lower == "--ipc=host" || (lower == "--ipc" && hasNext(args, arg, "host")):
			return fmt.Errorf("--ipc=host is not allowed")
		case strings.HasPrefix(lower, "--cap-add"):
			return fmt.Errorf("--cap-add is not allowed")
		case strings.HasPrefix(lower, "--device"):
			return fmt.Errorf("--device is not allowed")
		}
	}
	return nil
}

// hasNext checks if the argument after the given arg has the given value.
func hasNext(args []string, current, nextVal string) bool {
	for i, a := range args {
		if a == current && i+1 < len(args) && strings.ToLower(args[i+1]) == nextVal {
			return true
		}
	}
	return false
}

// validateVolumeMounts validates that all volume mount sources are within the workspace.
func validateVolumeMounts(workspace string, volumes []string) error {
	for _, v := range volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid volume mount format %q: expected src:dest", v)
		}
		src := parts[0]
		if _, err := executil.SafePath(workspace, src); err != nil {
			return fmt.Errorf("invalid volume source %q: %v", src, err)
		}
	}
	return nil
}

// containerNameRe matches valid container names and hex container IDs.
var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// validateContainerName checks that a container name/ID is safe.
func validateContainerName(name string) error {
	if name == "" {
		return fmt.Errorf("container name must not be empty")
	}
	if !containerNameRe.MatchString(name) {
		return fmt.Errorf("invalid container name %q", name)
	}
	return nil
}

// --- Output parsing ---

// parseJSONLines parses newline-delimited JSON objects from docker --format output.
func parseJSONLines(output string) []map[string]any {
	var results []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil {
			results = append(results, obj)
		}
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results
}

// parseDigest extracts a digest from docker pull/push output.
func parseDigest(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Digest:") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "Digest:" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
		if strings.Contains(line, "digest:") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "digest:" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

// parseImageID extracts the image ID from docker build output.
func parseImageID(output string) string {
	for _, line := range strings.Split(output, "\n") {
		// Traditional build: "Successfully built <id>"
		if strings.HasPrefix(line, "Successfully built ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Successfully built "))
		}
		// BuildKit: "writing image sha256:<id>"
		if strings.Contains(line, "writing image sha256:") {
			idx := strings.Index(line, "sha256:")
			if idx >= 0 {
				rest := line[idx:]
				// Trim at first space or end.
				if sp := strings.IndexByte(rest, ' '); sp > 0 {
					return rest[:sp]
				}
				return strings.TrimSpace(rest)
			}
		}
	}
	return ""
}

// --- Response helpers ---

func okResp(reqID string, result any) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, true, result, "", 0)
	return &resp
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

// --- Parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("parameter %s must be a non-empty string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	v, ok := params[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func intDefault(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(n)
		if err == nil {
			return parsed
		}
	}
	return def
}

func boolDefault(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func strSliceParam(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	if ss, ok := v.([]string); ok {
		return ss
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mapParam(params map[string]any, key string) map[string]string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprint(val)
		}
	}
	return out
}
