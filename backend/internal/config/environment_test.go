package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSetupEnvironmentConflicts(t *testing.T) {
	for _, pair := range setupRuntimeConnectionEnvPairs {
		pair := pair
		t.Run(pair[0], func(t *testing.T) {
			clearConnectionEnvironment(t)
			t.Setenv(pair[0], "setup-value")
			if err := ValidateSetupEnvironmentConflicts(); err != nil {
				t.Fatalf("setup-only variable returned error: %v", err)
			}

			clearConnectionEnvironment(t)
			t.Setenv(pair[1], "runtime-value")
			if err := ValidateSetupEnvironmentConflicts(); err != nil {
				t.Fatalf("runtime-only variable returned error: %v", err)
			}

			clearConnectionEnvironment(t)
			t.Setenv(pair[0], "same-value")
			t.Setenv(pair[1], "same-value")
			err := ValidateSetupEnvironmentConflicts()
			if err == nil {
				t.Fatal("expected duplicate environment variables to be rejected")
			}
			if !strings.Contains(err.Error(), pair[0]) || !strings.Contains(err.Error(), pair[1]) {
				t.Fatalf("error does not identify conflicting keys: %v", err)
			}
			if strings.Contains(err.Error(), "same-value") {
				t.Fatalf("error leaked environment value: %v", err)
			}
		})
	}
}

func TestLoadRejectsSetupRuntimeConnectionConflict(t *testing.T) {
	clearConnectionEnvironment(t)
	resetViperWithJWTSecret(t)
	t.Setenv("SETUP_DATABASE_HOST", "setup-db")
	t.Setenv("DATABASE_HOST", "runtime-db")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to reject setup/runtime environment conflict")
	}
	if !strings.Contains(err.Error(), "SETUP_DATABASE_HOST") || !strings.Contains(err.Error(), "DATABASE_HOST") {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestLoadSQLiteIgnoresMySQLOnlyPortAndSSLMode(t *testing.T) {
	clearConnectionEnvironment(t)
	resetViperWithJWTSecret(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PORT", "not-a-port")
	t.Setenv("DATABASE_SSLMODE", "legacy-invalid")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Port != 0 || cfg.Database.SSLMode != "" {
		t.Fatalf("unexpected sqlite MySQL-only fields: port=%d sslmode=%q", cfg.Database.Port, cfg.Database.SSLMode)
	}
}

// TestLoadMySQLRejectsInvalidPortAndSSLMode ensures MySQL-only settings remain strictly validated.
func TestLoadMySQLRejectsInvalidPortAndSSLMode(t *testing.T) {
	t.Run("port", func(t *testing.T) {
		clearConnectionEnvironment(t)
		resetViperWithJWTSecret(t)
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("DATABASE_DRIVER", "mysql")
		t.Setenv("DATABASE_PORT", "not-a-port")

		if _, err := Load(); err == nil {
			t.Fatal("expected invalid MySQL port to be rejected")
		}
	})

	t.Run("sslmode", func(t *testing.T) {
		clearConnectionEnvironment(t)
		resetViperWithJWTSecret(t)
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("DATABASE_DRIVER", "mysql")
		t.Setenv("DATABASE_PORT", "3306")
		t.Setenv("DATABASE_SSLMODE", "legacy-invalid")

		_, err := Load()
		if err == nil {
			t.Fatal("expected invalid MySQL sslmode to be rejected")
		}
		if !strings.Contains(err.Error(), "database.sslmode") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadSQLiteIgnoresMySQLOnlyValuesFromConfigFile(t *testing.T) {
	clearConnectionEnvironment(t)
	resetViperWithJWTSecret(t)
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	configYAML := "database:\n  driver: sqlite\n  port: not-a-port\n  sslmode: legacy-invalid\n"
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml): %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Port != 0 || cfg.Database.SSLMode != "" {
		t.Fatalf("unexpected sqlite MySQL-only fields: port=%d sslmode=%q", cfg.Database.Port, cfg.Database.SSLMode)
	}
}

func TestNormalizeDatabaseSSLMode(t *testing.T) {
	for input, want := range map[string]string{
		"":             "disable",
		" disable ":    "disable",
		"REQUIRE":      "require",
		"verify-ca":    "verify-ca",
		"VERIFY-FULL ": "verify-full",
	} {
		got, err := NormalizeDatabaseSSLMode(input)
		if err != nil {
			t.Fatalf("NormalizeDatabaseSSLMode(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeDatabaseSSLMode(%q)=%q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"prefer", "preferred", "invalid"} {
		if _, err := NormalizeDatabaseSSLMode(input); err == nil {
			t.Fatalf("NormalizeDatabaseSSLMode(%q) expected error", input)
		}
	}
}

// clearConnectionEnvironment isolates connection-related environment variables for a test.
func clearConnectionEnvironment(t *testing.T) {
	t.Helper()
	for _, pair := range setupRuntimeConnectionEnvPairs {
		for _, key := range pair {
			value, existed := os.LookupEnv(key)
			if err := os.Unsetenv(key); err != nil {
				t.Fatalf("Unsetenv(%s): %v", key, err)
			}
			key, value, existed := key, value, existed
			t.Cleanup(func() {
				if existed {
					_ = os.Setenv(key, value)
				} else {
					_ = os.Unsetenv(key)
				}
			})
		}
	}
}
