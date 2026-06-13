package sandbox

import "testing"

func TestSandboxConfigForTier_Reader(t *testing.T) {
	cfg := SandboxConfigForTier("reader", DefaultSandboxConfig())

	if cfg.MaxMemoryMB != 256 {
		t.Errorf("reader MaxMemoryMB = %d, want 256", cfg.MaxMemoryMB)
	}
	if cfg.TimeoutSeconds != 10 {
		t.Errorf("reader TimeoutSeconds = %d, want 10", cfg.TimeoutSeconds)
	}
	if !cfg.AllowNetwork {
		t.Error("reader AllowNetwork should be true")
	}
	if cfg.MaxCPUPercent != 10 {
		t.Errorf("reader MaxCPUPercent = %d, want 10", cfg.MaxCPUPercent)
	}
}

func TestSandboxConfigForTier_Writer(t *testing.T) {
	cfg := SandboxConfigForTier("writer", DefaultSandboxConfig())

	if cfg.MaxMemoryMB != 512 {
		t.Errorf("writer MaxMemoryMB = %d, want 512", cfg.MaxMemoryMB)
	}
	if cfg.TimeoutSeconds != 30 {
		t.Errorf("writer TimeoutSeconds = %d, want 30", cfg.TimeoutSeconds)
	}
	if !cfg.AllowNetwork {
		t.Error("writer AllowNetwork should be true")
	}
	if cfg.MaxCPUPercent != 25 {
		t.Errorf("writer MaxCPUPercent = %d, want 25", cfg.MaxCPUPercent)
	}
}

func TestSandboxConfigForTier_Operator(t *testing.T) {
	defaults := DefaultSandboxConfig()
	cfg := SandboxConfigForTier("operator", defaults)

	if cfg.MaxMemoryMB != defaults.MaxMemoryMB {
		t.Errorf("operator MaxMemoryMB = %d, want %d", cfg.MaxMemoryMB, defaults.MaxMemoryMB)
	}
	if cfg.TimeoutSeconds != defaults.TimeoutSeconds {
		t.Errorf("operator TimeoutSeconds = %d, want %d", cfg.TimeoutSeconds, defaults.TimeoutSeconds)
	}
	if !cfg.AllowNetwork {
		t.Error("operator AllowNetwork should be true")
	}
}

func TestSandboxConfigForTier_Admin(t *testing.T) {
	cfg := SandboxConfigForTier("admin", DefaultSandboxConfig())

	if cfg.MaxMemoryMB != 1024 {
		t.Errorf("admin MaxMemoryMB = %d, want 1024", cfg.MaxMemoryMB)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("admin TimeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}
	if !cfg.AllowNetwork {
		t.Error("admin AllowNetwork should be true")
	}
	if cfg.MaxCPUPercent != 50 {
		t.Errorf("admin MaxCPUPercent = %d, want 50", cfg.MaxCPUPercent)
	}
}

func TestSandboxConfigForTier_UnknownDefaultsToStrictest(t *testing.T) {
	cfg := SandboxConfigForTier("bogus", DefaultSandboxConfig())

	if cfg.MaxMemoryMB != 256 {
		t.Errorf("unknown MaxMemoryMB = %d, want 256", cfg.MaxMemoryMB)
	}
	if cfg.TimeoutSeconds != 10 {
		t.Errorf("unknown TimeoutSeconds = %d, want 10", cfg.TimeoutSeconds)
	}
	if cfg.AllowNetwork {
		t.Error("unknown AllowNetwork should be false")
	}
	if cfg.MaxCPUPercent != 10 {
		t.Errorf("unknown MaxCPUPercent = %d, want 10", cfg.MaxCPUPercent)
	}
}

func TestSandboxConfigForTier_EmptyTier(t *testing.T) {
	cfg := SandboxConfigForTier("", DefaultSandboxConfig())

	if cfg.MaxMemoryMB != 256 {
		t.Errorf("empty tier MaxMemoryMB = %d, want 256", cfg.MaxMemoryMB)
	}
}
