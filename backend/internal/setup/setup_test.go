package setup

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRegisterRoutesIncludesHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /health status=%d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `"mode":"setup"`) {
		t.Fatalf("GET /health response=%q, want setup mode", recorder.Body.String())
	}
}

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

func TestSetupEnvironmentPrefersSetupOnlyVariables(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "mysql")
	t.Setenv("SETUP_DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PORT", "3306")
	t.Setenv("SETUP_DATABASE_PORT", "4406")

	if got := getSetupEnvOrDefault("DATABASE_DRIVER", "mysql"); got != "sqlite" {
		t.Fatalf("getSetupEnvOrDefault()=%q, want sqlite", got)
	}
	gotPort, err := getSetupEnvIntOrDefault("DATABASE_PORT", 3306)
	if err != nil {
		t.Fatalf("getSetupEnvIntOrDefault() error = %v", err)
	}
	if gotPort != 4406 {
		t.Fatalf("getSetupEnvIntOrDefault()=%d, want 4406", gotPort)
	}
}

func TestSetupEnvironmentFallsBackToLegacyVariables(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PORT", "5506")
	unsetEnvForTest(t, "SETUP_DATABASE_DRIVER")
	unsetEnvForTest(t, "SETUP_DATABASE_PORT")

	if got := getSetupEnvOrDefault("DATABASE_DRIVER", "mysql"); got != "sqlite" {
		t.Fatalf("getSetupEnvOrDefault()=%q, want sqlite", got)
	}
	gotPort, err := getSetupEnvIntOrDefault("DATABASE_PORT", 3306)
	if err != nil {
		t.Fatalf("getSetupEnvIntOrDefault() error = %v", err)
	}
	if gotPort != 5506 {
		t.Fatalf("getSetupEnvIntOrDefault()=%d, want 5506", gotPort)
	}
}

func TestSetupEnvironmentSupportsExplicitEmptyValue(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "legacy-secret")
	t.Setenv("SETUP_DATABASE_PASSWORD", "")
	if got := getSetupEnvOrDefault("DATABASE_PASSWORD", "default-secret"); got != "" {
		t.Fatalf("getSetupEnvOrDefault()=%q, want explicit empty value", got)
	}
}

func TestSetupEnvironmentRejectsInvalidTypedValues(t *testing.T) {
	t.Setenv("SETUP_DATABASE_PORT", "not-a-port")
	if _, err := getSetupEnvIntOrDefault("DATABASE_PORT", 3306); err == nil || !strings.Contains(err.Error(), "SETUP_DATABASE_PORT") {
		t.Fatalf("expected SETUP_DATABASE_PORT parse error, got %v", err)
	}

	t.Setenv("SETUP_REDIS_ENABLE_TLS", "sometimes")
	if _, err := getSetupEnvBoolOrDefault("REDIS_ENABLE_TLS", false); err == nil || !strings.Contains(err.Error(), "SETUP_REDIS_ENABLE_TLS") {
		t.Fatalf("expected SETUP_REDIS_ENABLE_TLS parse error, got %v", err)
	}
}

func TestSQLiteSetupIgnoresMySQLOnlyEnvironment(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SETUP_DATABASE_DRIVER", "sqlite")
	t.Setenv("SETUP_DATABASE_PORT", "not-a-port")
	t.Setenv("SETUP_DATABASE_SSLMODE", "legacy-invalid")

	cfg, err := databaseConfigFromSetupEnvironment()
	if err != nil {
		t.Fatalf("databaseConfigFromSetupEnvironment() error = %v", err)
	}
	if cfg.Driver != "sqlite" || cfg.Port != 0 || cfg.SSLMode != "" {
		t.Fatalf("unexpected sqlite config: %+v", cfg)
	}
}

func TestMySQLSetupRejectsInvalidPortAndSSLMode(t *testing.T) {
	t.Setenv("SETUP_DATABASE_DRIVER", "mysql")
	t.Setenv("SETUP_DATABASE_PORT", "not-a-port")
	if _, err := databaseConfigFromSetupEnvironment(); err == nil || !strings.Contains(err.Error(), "SETUP_DATABASE_PORT") {
		t.Fatalf("expected invalid MySQL port error, got %v", err)
	}

	t.Setenv("SETUP_DATABASE_PORT", "3306")
	t.Setenv("SETUP_DATABASE_SSLMODE", "legacy-invalid")
	if _, err := databaseConfigFromSetupEnvironment(); err == nil || !strings.Contains(err.Error(), "database.sslmode") {
		t.Fatalf("expected invalid MySQL sslmode error, got %v", err)
	}
}

func TestAutoSetupRejectsSetupAndRuntimeConnectionVariables(t *testing.T) {
	t.Setenv("SETUP_DATABASE_PASSWORD", "setup-secret")
	t.Setenv("DATABASE_PASSWORD", "runtime-secret")
	err := AutoSetupFromEnv()
	if err == nil {
		t.Fatal("expected conflicting setup/runtime variables to be rejected")
	}
	if !strings.Contains(err.Error(), "SETUP_DATABASE_PASSWORD") || !strings.Contains(err.Error(), "DATABASE_PASSWORD") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "setup-secret") || strings.Contains(err.Error(), "runtime-secret") {
		t.Fatalf("error leaked secret values: %v", err)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%s): %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
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

func TestNormalizeDatabaseConfigDefaultsToSQLite(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	cfg := &DatabaseConfig{}

	if err := normalizeDatabaseConfig(cfg); err != nil {
		t.Fatalf("normalizeDatabaseConfig() error = %v", err)
	}
	if cfg.Driver != "sqlite" {
		t.Fatalf("driver=%q, want sqlite", cfg.Driver)
	}
	if wantPath := filepath.Join(dataDir, "ikik-api.db"); cfg.Path != wantPath {
		t.Fatalf("path=%q, want %q", cfg.Path, wantPath)
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
