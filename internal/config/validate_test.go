package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSandboxConfig_ValidCombos(t *testing.T) {
	tests := []struct {
		name       string
		hostAccess string
		allowUnr   *bool
		wantErr    bool
	}{
		{"sandbox default", "sandbox", boolPtr(false), false},
		{"host without unrestricted", "host", boolPtr(false), false},
		{"host with unrestricted", "host", boolPtr(true), false},
		{"sandbox with unrestricted (invalid)", "sandbox", boolPtr(true), true},
		{"invalid mode", "invalid", boolPtr(false), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Sandbox.HostAccess = tt.hostAccess
			cfg.Sandbox.AllowUnrestricted = tt.allowUnr
			err := validateSandboxConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSandboxConfig() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSandboxConfig_ExtraPaths(t *testing.T) {
	t.Run("absolute paths accepted", func(t *testing.T) {
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		cfg.Sandbox.ExtraPaths = ExtraPathsConfig{
			ReadWrite: []string{"/mnt/nfs/shared"},
			ReadOnly:  []string{"/mnt/nfs/reference"},
		}
		if err := validateSandboxConfig(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("relative read_write path rejected", func(t *testing.T) {
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		cfg.Sandbox.ExtraPaths = ExtraPathsConfig{
			ReadWrite: []string{"relative/path"},
		}
		if err := validateSandboxConfig(cfg); err == nil {
			t.Fatal("expected error for relative path")
		}
	})

	t.Run("relative read_only path rejected", func(t *testing.T) {
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		cfg.Sandbox.ExtraPaths = ExtraPathsConfig{
			ReadOnly: []string{"relative/path"},
		}
		if err := validateSandboxConfig(cfg); err == nil {
			t.Fatal("expected error for relative path")
		}
	})
}

func TestValidateHostAccess_ProbesRuntime(t *testing.T) {
	t.Run("sandbox mode basic", func(t *testing.T) {
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		diag := ValidateHostAccess(cfg)
		if diag.Mode != "sandbox" {
			t.Errorf("expected mode sandbox, got %s", diag.Mode)
		}
	})

	t.Run("inaccessible extra_paths warned", func(t *testing.T) {
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		cfg.Sandbox.ExtraPaths = ExtraPathsConfig{
			ReadWrite: []string{"/nonexistent/path/that/does/not/exist"},
		}
		diag := ValidateHostAccess(cfg)
		if len(diag.Warnings) == 0 {
			t.Fatal("expected warning for inaccessible path")
		}
		if len(diag.InaccessiblePaths) == 0 {
			t.Fatal("expected inaccessible path recorded")
		}
	})

	t.Run("accessible extra_paths no warning", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{}
		cfg.Sandbox.HostAccess = "sandbox"
		cfg.Sandbox.AllowUnrestricted = boolPtr(false)
		cfg.Sandbox.ExtraPaths = ExtraPathsConfig{
			ReadOnly: []string{tmpDir},
		}
		diag := ValidateHostAccess(cfg)
		if len(diag.InaccessiblePaths) != 0 {
			t.Fatalf("expected no inaccessible paths, got %v", diag.InaccessiblePaths)
		}
	})
}

func TestLoad_SandboxDefaults(t *testing.T) {
	// Write a minimal config to a temp file.
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "kyvik.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  listen_addr: \":9999\"\nstorage:\n  driver: postgres\n  postgres:\n    dsn: \"postgres://kyvik:kyvik@localhost:5432/kyvik?sslmode=disable\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Sandbox.HostAccess != "sandbox" {
		t.Errorf("expected host_access=sandbox, got %q", cfg.Sandbox.HostAccess)
	}
	if cfg.Sandbox.AllowUnrestricted == nil {
		t.Fatal("expected AllowUnrestricted to be set")
	}
	if *cfg.Sandbox.AllowUnrestricted {
		t.Error("expected AllowUnrestricted=false by default")
	}
}

func TestLoad_DBDSNEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "kyvik.yaml")

	t.Run("switches driver to postgres when no driver set", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("server:\n  listen_addr: \":9999\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KYVIK_DB_DSN", "postgres://kyvik:kyvik@localhost:5432/kyvik?sslmode=disable")

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Storage.Driver != "postgres" {
			t.Errorf("expected driver=postgres, got %q", cfg.Storage.Driver)
		}
		if cfg.Storage.Postgres.DSN != "postgres://kyvik:kyvik@localhost:5432/kyvik?sslmode=disable" {
			t.Errorf("expected DSN from env, got %q", cfg.Storage.Postgres.DSN)
		}
	})

	t.Run("overrides DSN when driver already postgres", func(t *testing.T) {
		yaml := "storage:\n  driver: postgres\n  postgres:\n    dsn: \"postgres://old@localhost/old\"\n"
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KYVIK_DB_DSN", "postgres://new@localhost/new")

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Storage.Driver != "postgres" {
			t.Errorf("expected driver=postgres, got %q", cfg.Storage.Driver)
		}
		if cfg.Storage.Postgres.DSN != "postgres://new@localhost/new" {
			t.Errorf("expected DSN from env, got %q", cfg.Storage.Postgres.DSN)
		}
	})

	t.Run("no env var defaults to postgres", func(t *testing.T) {
		yaml := "storage:\n  postgres:\n    dsn: \"postgres://test@localhost/test\"\n"
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KYVIK_DB_DSN", "")

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Storage.Driver != "postgres" {
			t.Errorf("expected driver=postgres, got %q", cfg.Storage.Driver)
		}
	})

	t.Run("no DSN errors with helpful message", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("server:\n  listen_addr: \":9999\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KYVIK_DB_DSN", "")

		_, err := Load(cfgPath)
		if err == nil {
			t.Fatal("expected error when postgres is default but no DSN set")
		}
	})

	t.Run("non-postgres driver returns error", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("storage:\n  driver: sqlite\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KYVIK_DB_DSN", "")

		_, err := Load(cfgPath)
		if err == nil {
			t.Fatal("expected error for unsupported sqlite driver")
		}
	})
}

func boolPtr(b bool) *bool { return &b }
