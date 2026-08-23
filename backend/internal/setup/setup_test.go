package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecideAdminBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalUsers int64
		adminUsers int64
		should     bool
		reason     string
	}{
		{
			name:       "empty database should create admin",
			totalUsers: 0,
			adminUsers: 0,
			should:     true,
			reason:     adminBootstrapReasonEmptyDatabase,
		},
		{
			name:       "admin exists should skip",
			totalUsers: 10,
			adminUsers: 1,
			should:     false,
			reason:     adminBootstrapReasonAdminExists,
		},
		{
			name:       "users exist without admin should skip",
			totalUsers: 5,
			adminUsers: 0,
			should:     false,
			reason:     adminBootstrapReasonUsersExistWithoutAdmin,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideAdminBootstrap(tc.totalUsers, tc.adminUsers)
			if got.shouldCreate != tc.should {
				t.Fatalf("shouldCreate=%v, want %v", got.shouldCreate, tc.should)
			}
			if got.reason != tc.reason {
				t.Fatalf("reason=%q, want %q", got.reason, tc.reason)
			}
		})
	}
}

func TestSetupDefaultAdminConcurrency(t *testing.T) {
	t.Run("simple mode admin uses higher concurrency", func(t *testing.T) {
		t.Setenv("RUN_MODE", "simple")
		if got := setupDefaultAdminConcurrency(); got != simpleModeAdminConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, simpleModeAdminConcurrency)
		}
	})

	t.Run("standard mode keeps existing default", func(t *testing.T) {
		t.Setenv("RUN_MODE", "standard")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})
}

func TestSetupMigrationTimeout(t *testing.T) {
	t.Run("uses default timeout when unset", func(t *testing.T) {
		cfg := &SetupConfig{}
		if got := cfg.migrationTimeout(); got != defaultMigrationTimeout {
			t.Fatalf("migrationTimeout()=%s, want %s", got, defaultMigrationTimeout)
		}
	})

	t.Run("uses configured timeout", func(t *testing.T) {
		cfg := &SetupConfig{MigrationTimeoutSeconds: 300}
		if got := cfg.migrationTimeout(); got != 300*time.Second {
			t.Fatalf("migrationTimeout()=%s, want 300s", got)
		}
	})
}

func TestWriteConfigFileKeepsDefaultUserConcurrency(t *testing.T) {
	t.Setenv("RUN_MODE", "simple")
	t.Setenv("DATA_DIR", t.TempDir())

	cfg := &SetupConfig{
		Totp: TotpConfig{
			EncryptionKey: strings.Repeat("a", 64),
		},
	}
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "user_concurrency: 5") {
		t.Fatalf("config missing default user concurrency, got:\n%s", string(data))
	}
}

func TestWriteConfigFilePersistsTotpEncryptionKey(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	key := strings.Repeat("b", 64)
	if err := writeConfigFile(&SetupConfig{
		Totp: TotpConfig{
			EncryptionKey: key,
		},
	}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "totp:") || !strings.Contains(content, "encryption_key: "+key) {
		t.Fatalf("config missing totp encryption key, got:\n%s", content)
	}
}

func TestSQLiteSetupLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	cfg := &SetupConfig{
		Database: DatabaseConfig{Driver: "sqlite", Path: "nested/app.db"},
		Admin:    AdminConfig{Email: "admin@example.com", Password: "password123"},
	}
	if err := TestDatabaseConnection(&cfg.Database); err != nil {
		t.Fatalf("TestDatabaseConnection() error = %v", err)
	}
	wantPath := filepath.Join(dataDir, "nested", "app.db")
	if cfg.Database.Path != wantPath {
		t.Fatalf("path=%q, want %q", cfg.Database.Path, wantPath)
	}
	if err := initializeDatabase(cfg); err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	created, _, err := createAdminUser(cfg)
	if err != nil {
		t.Fatalf("createAdminUser() error = %v", err)
	}
	if !created {
		t.Fatal("admin was not created")
	}
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}
	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "driver: sqlite") || !strings.Contains(content, filepath.ToSlash(wantPath)) && !strings.Contains(content, wantPath) {
		t.Fatalf("config missing sqlite settings:\n%s", content)
	}
}

func TestSQLitePathMustStayInsideDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	cfg := &DatabaseConfig{Driver: "sqlite", Path: filepath.Join("..", "outside.db")}
	if err := normalizeDatabaseConfig(cfg); err == nil {
		t.Fatal("expected path traversal error")
	}
}
