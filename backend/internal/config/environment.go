package config

import (
	"fmt"
	"os"
	"strings"
)

var setupRuntimeConnectionEnvPairs = [][2]string{
	{"SETUP_DATABASE_DRIVER", "DATABASE_DRIVER"},
	{"SETUP_DATABASE_PATH", "DATABASE_PATH"},
	{"SETUP_DATABASE_HOST", "DATABASE_HOST"},
	{"SETUP_DATABASE_PORT", "DATABASE_PORT"},
	{"SETUP_DATABASE_USER", "DATABASE_USER"},
	{"SETUP_DATABASE_PASSWORD", "DATABASE_PASSWORD"},
	{"SETUP_DATABASE_DBNAME", "DATABASE_DBNAME"},
	{"SETUP_DATABASE_SSLMODE", "DATABASE_SSLMODE"},
	{"SETUP_REDIS_HOST", "REDIS_HOST"},
	{"SETUP_REDIS_PORT", "REDIS_PORT"},
	{"SETUP_REDIS_PASSWORD", "REDIS_PASSWORD"},
	{"SETUP_REDIS_DB", "REDIS_DB"},
	{"SETUP_REDIS_ENABLE_TLS", "REDIS_ENABLE_TLS"},
}

// ValidateSetupEnvironmentConflicts prevents setup and runtime variables from selecting different stores.
func ValidateSetupEnvironmentConflicts() error {
	for _, pair := range setupRuntimeConnectionEnvPairs {
		_, setupDefined := os.LookupEnv(pair[0])
		_, runtimeDefined := os.LookupEnv(pair[1])
		if setupDefined && runtimeDefined {
			return fmt.Errorf("conflicting environment variables: %s and %s cannot be defined together", pair[0], pair[1])
		}
	}
	return nil
}

// NormalizeDatabaseSSLMode returns the supported MySQL TLS policy name.
func NormalizeDatabaseSSLMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = "disable"
	}
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
		return mode, nil
	default:
		return "", fmt.Errorf("database.sslmode must be one of: disable/require/verify-ca/verify-full")
	}
}
