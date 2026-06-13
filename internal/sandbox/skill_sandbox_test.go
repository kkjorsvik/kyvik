package sandbox

import (
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- intersectHosts tests ---

func TestIntersectHosts_BothEmpty(t *testing.T) {
	result := intersectHosts(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestIntersectHosts_AgentEmpty_ReturnsSkillList(t *testing.T) {
	// Agent has no restrictions (empty = all allowed), skill specifies hosts.
	skillHosts := []string{"api.example.com", "cdn.example.com"}
	result := intersectHosts(nil, skillHosts)
	if len(result) != 2 {
		t.Fatalf("expected 2 hosts (skill list), got %d: %v", len(result), result)
	}
	if result[0] != "api.example.com" || result[1] != "cdn.example.com" {
		t.Errorf("expected skill hosts, got %v", result)
	}
}

func TestIntersectHosts_SkillEmpty_ReturnsAgentList(t *testing.T) {
	// Agent has restrictions, skill has none.
	agentHosts := []string{"api.example.com", "db.internal.com"}
	result := intersectHosts(agentHosts, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 hosts (agent list), got %d: %v", len(result), result)
	}
}

func TestIntersectHosts_Intersection(t *testing.T) {
	agentHosts := []string{"api.example.com", "db.internal.com", "cdn.example.com"}
	skillHosts := []string{"api.example.com", "cdn.example.com", "other.com"}

	result := intersectHosts(agentHosts, skillHosts)
	if len(result) != 2 {
		t.Fatalf("expected 2 hosts (intersection), got %d: %v", len(result), result)
	}

	// Verify the intersection contains only hosts in both lists.
	resultSet := make(map[string]bool)
	for _, h := range result {
		resultSet[h] = true
	}
	if !resultSet["api.example.com"] {
		t.Error("expected api.example.com in intersection")
	}
	if !resultSet["cdn.example.com"] {
		t.Error("expected cdn.example.com in intersection")
	}
	if resultSet["db.internal.com"] {
		t.Error("db.internal.com should not be in intersection")
	}
	if resultSet["other.com"] {
		t.Error("other.com should not be in intersection")
	}
}

func TestIntersectHosts_NoOverlap(t *testing.T) {
	agentHosts := []string{"a.com", "b.com"}
	skillHosts := []string{"c.com", "d.com"}

	result := intersectHosts(agentHosts, skillHosts)
	if len(result) != 0 {
		t.Errorf("expected empty intersection, got %v", result)
	}
}

func TestIntersectHosts_WhitespaceHandling(t *testing.T) {
	agentHosts := []string{"api.example.com"}
	skillHosts := []string{" api.example.com "}

	result := intersectHosts(agentHosts, skillHosts)
	if len(result) != 1 {
		t.Fatalf("expected 1 host (trimmed match), got %d: %v", len(result), result)
	}
}

// --- buildEnvironment with skill config tests ---

func TestBuildEnvironment_NilSkillConfig(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	sb.HTTPAllowedHosts = []string{"api.example.com"}

	env := buildEnvironment(sb, nil)

	envMap := envToMap(env)

	// Agent hosts should pass through unchanged.
	if envMap["KYVIK_HTTP_ALLOWED_HOSTS"] != "api.example.com" {
		t.Errorf("expected KYVIK_HTTP_ALLOWED_HOSTS=api.example.com, got %q", envMap["KYVIK_HTTP_ALLOWED_HOSTS"])
	}

	// No skill paths should be set.
	if _, ok := envMap["KYVIK_SKILL_READ_PATHS"]; ok {
		t.Error("expected no KYVIK_SKILL_READ_PATHS with nil skill config")
	}
	if _, ok := envMap["KYVIK_SKILL_WRITE_PATHS"]; ok {
		t.Error("expected no KYVIK_SKILL_WRITE_PATHS with nil skill config")
	}
}

func TestBuildEnvironment_SkillHostIntersection(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	sb.HTTPAllowedHosts = []string{"api.example.com", "cdn.example.com", "db.internal.com"}

	skillCfg := &types.SkillSandboxConfig{
		AllowedHosts: []string{"api.example.com", "cdn.example.com"},
	}

	env := buildEnvironment(sb, skillCfg)
	envMap := envToMap(env)

	hosts := envMap["KYVIK_HTTP_ALLOWED_HOSTS"]
	if hosts == "" {
		t.Fatal("expected KYVIK_HTTP_ALLOWED_HOSTS to be set")
	}

	hostList := strings.Split(hosts, ",")
	hostSet := make(map[string]bool)
	for _, h := range hostList {
		hostSet[h] = true
	}

	if !hostSet["api.example.com"] || !hostSet["cdn.example.com"] {
		t.Errorf("expected intersection hosts, got %q", hosts)
	}
	if hostSet["db.internal.com"] {
		t.Errorf("db.internal.com should not be in intersection, got %q", hosts)
	}
}

func TestBuildEnvironment_SkillHostIntersection_AgentUnrestricted(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	// Agent has no host restrictions (empty = all allowed).
	sb.HTTPAllowedHosts = nil

	skillCfg := &types.SkillSandboxConfig{
		AllowedHosts: []string{"api.example.com"},
	}

	env := buildEnvironment(sb, skillCfg)
	envMap := envToMap(env)

	// Skill hosts should be used directly when agent has no restrictions.
	hosts := envMap["KYVIK_HTTP_ALLOWED_HOSTS"]
	if hosts != "api.example.com" {
		t.Errorf("expected KYVIK_HTTP_ALLOWED_HOSTS=api.example.com, got %q", hosts)
	}
}

func TestBuildEnvironment_SkillReadWritePaths(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")

	skillCfg := &types.SkillSandboxConfig{
		ReadPaths:  []string{"data/", "config/settings.yaml"},
		WritePaths: []string{"output/"},
	}

	env := buildEnvironment(sb, skillCfg)
	envMap := envToMap(env)

	readPaths := envMap["KYVIK_SKILL_READ_PATHS"]
	if readPaths != "data/,config/settings.yaml" {
		t.Errorf("expected KYVIK_SKILL_READ_PATHS=data/,config/settings.yaml, got %q", readPaths)
	}

	writePaths := envMap["KYVIK_SKILL_WRITE_PATHS"]
	if writePaths != "output/" {
		t.Errorf("expected KYVIK_SKILL_WRITE_PATHS=output/, got %q", writePaths)
	}
}

func TestBuildEnvironment_SkillNoPathRestrictions(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")

	skillCfg := &types.SkillSandboxConfig{
		AllowNetwork: true,
		// No path restrictions.
	}

	env := buildEnvironment(sb, skillCfg)
	envMap := envToMap(env)

	if _, ok := envMap["KYVIK_SKILL_READ_PATHS"]; ok {
		t.Error("expected no KYVIK_SKILL_READ_PATHS when skill has no read paths")
	}
	if _, ok := envMap["KYVIK_SKILL_WRITE_PATHS"]; ok {
		t.Error("expected no KYVIK_SKILL_WRITE_PATHS when skill has no write paths")
	}
}

func TestBuildEnvironment_SkillHostNoOverlap(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	sb.HTTPAllowedHosts = []string{"a.com"}

	skillCfg := &types.SkillSandboxConfig{
		AllowedHosts: []string{"b.com"},
	}

	env := buildEnvironment(sb, skillCfg)
	envMap := envToMap(env)

	// No overlap → no hosts should be allowed.
	if _, ok := envMap["KYVIK_HTTP_ALLOWED_HOSTS"]; ok {
		t.Errorf("expected no KYVIK_HTTP_ALLOWED_HOSTS when intersection is empty, got %q", envMap["KYVIK_HTTP_ALLOWED_HOSTS"])
	}
}

// envToMap converts a list of KEY=VALUE env vars to a map.
func envToMap(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}
