package ktp

import (
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestApplySkillConstraints_NilSkillConfig(t *testing.T) {
	overrides := map[string]any{
		"allow_network": true,
		"max_memory_mb": 1024,
	}
	applySkillConstraints(overrides, nil)

	// Should not be changed.
	if overrides["allow_network"] != true {
		t.Errorf("expected allow_network to remain true, got %v", overrides["allow_network"])
	}
	if overrides["max_memory_mb"] != 1024 {
		t.Errorf("expected max_memory_mb to remain 1024, got %v", overrides["max_memory_mb"])
	}
}

func TestApplySkillConstraints_DisableNetwork(t *testing.T) {
	overrides := map[string]any{
		"allow_network": true,
	}
	skill := &types.SkillSandboxConfig{
		AllowNetwork: false,
	}
	applySkillConstraints(overrides, skill)

	if overrides["allow_network"] != false {
		t.Errorf("expected allow_network=false (skill restricts), got %v", overrides["allow_network"])
	}
}

func TestApplySkillConstraints_CannotEnableNetwork(t *testing.T) {
	overrides := map[string]any{
		"allow_network": false,
	}
	skill := &types.SkillSandboxConfig{
		AllowNetwork: true, // Skill says true, but can't expand.
	}
	applySkillConstraints(overrides, skill)

	// Network should remain false — skill cannot expand agent permissions.
	if overrides["allow_network"] != false {
		t.Errorf("expected allow_network to remain false (skill cannot expand), got %v", overrides["allow_network"])
	}
}

func TestApplySkillConstraints_AllowedHosts(t *testing.T) {
	overrides := map[string]any{}
	skill := &types.SkillSandboxConfig{
		AllowedHosts: []string{"api.example.com", "cdn.example.com"},
	}
	applySkillConstraints(overrides, skill)

	hosts, ok := overrides["skill_allowed_hosts"].([]string)
	if !ok {
		t.Fatalf("expected skill_allowed_hosts to be []string, got %T", overrides["skill_allowed_hosts"])
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	if hosts[0] != "api.example.com" || hosts[1] != "cdn.example.com" {
		t.Fatalf("unexpected hosts: %v", hosts)
	}
}

func TestApplySkillConstraints_EmptyAllowedHosts(t *testing.T) {
	overrides := map[string]any{}
	skill := &types.SkillSandboxConfig{
		AllowedHosts: []string{},
	}
	applySkillConstraints(overrides, skill)

	if _, ok := overrides["skill_allowed_hosts"]; ok {
		t.Error("expected skill_allowed_hosts to not be set for empty list")
	}
}

func TestApplySkillConstraints_ReadPaths(t *testing.T) {
	overrides := map[string]any{}
	skill := &types.SkillSandboxConfig{
		ReadPaths: []string{"data/", "config/"},
	}
	applySkillConstraints(overrides, skill)

	paths, ok := overrides["skill_read_paths"].([]string)
	if !ok {
		t.Fatalf("expected skill_read_paths to be []string, got %T", overrides["skill_read_paths"])
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
}

func TestApplySkillConstraints_WritePaths(t *testing.T) {
	overrides := map[string]any{}
	skill := &types.SkillSandboxConfig{
		WritePaths: []string{"output/"},
	}
	applySkillConstraints(overrides, skill)

	paths, ok := overrides["skill_write_paths"].([]string)
	if !ok {
		t.Fatalf("expected skill_write_paths to be []string, got %T", overrides["skill_write_paths"])
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths))
	}
	if paths[0] != "output/" {
		t.Fatalf("expected 'output/', got %q", paths[0])
	}
}

func TestApplySkillConstraints_AllFieldsTogether(t *testing.T) {
	overrides := map[string]any{
		"allow_network":   true,
		"max_memory_mb":   2048,
		"timeout_seconds": 300,
	}
	skill := &types.SkillSandboxConfig{
		AllowNetwork: false,
		AllowedHosts: []string{"api.example.com"},
		ReadPaths:    []string{"data/"},
		WritePaths:   []string{"output/"},
	}
	applySkillConstraints(overrides, skill)

	if overrides["allow_network"] != false {
		t.Errorf("expected allow_network=false, got %v", overrides["allow_network"])
	}
	// Non-skill overrides should be unchanged.
	if overrides["max_memory_mb"] != 2048 {
		t.Errorf("expected max_memory_mb unchanged, got %v", overrides["max_memory_mb"])
	}
	if overrides["timeout_seconds"] != 300 {
		t.Errorf("expected timeout_seconds unchanged, got %v", overrides["timeout_seconds"])
	}
	if _, ok := overrides["skill_allowed_hosts"]; !ok {
		t.Error("expected skill_allowed_hosts to be set")
	}
	if _, ok := overrides["skill_read_paths"]; !ok {
		t.Error("expected skill_read_paths to be set")
	}
	if _, ok := overrides["skill_write_paths"]; !ok {
		t.Error("expected skill_write_paths to be set")
	}
}

func TestApplySkillConstraints_NetworkAlreadyDisabled(t *testing.T) {
	overrides := map[string]any{
		"allow_network": false,
	}
	skill := &types.SkillSandboxConfig{
		AllowNetwork: false, // Redundant, but should not cause issues.
	}
	applySkillConstraints(overrides, skill)

	if overrides["allow_network"] != false {
		t.Errorf("expected allow_network=false, got %v", overrides["allow_network"])
	}
}
