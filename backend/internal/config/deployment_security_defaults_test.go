package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeploymentSecurityDefaultsMatchApplicationDefaults(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))

	composeFiles := []string{
		"docker-compose.yml",
		"docker-compose.mysql.yml",
		"docker-compose.local.yml",
		"docker-compose.local.mysql.yml",
		"docker-compose.dev.yml",
		"docker-compose.dev.mysql.yml",
		"docker-compose.standalone.yml",
	}
	composeExpected := []string{
		"SECURITY_URL_ALLOWLIST_ENABLED=${SECURITY_URL_ALLOWLIST_ENABLED:-false}",
		"SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=${SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP:-true}",
		"SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=${SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS:-true}",
		"SECURITY_URL_ALLOWLIST_UPSTREAM_HOSTS=${SECURITY_URL_ALLOWLIST_UPSTREAM_HOSTS:-",
	}
	for _, name := range composeFiles {
		assertFileContainsAll(t, filepath.Join(repoRoot, "deploy", name), composeExpected)
	}

	envFiles := []string{".env.example", ".env.standalone.example"}
	envExpected := []string{
		"SECURITY_URL_ALLOWLIST_ENABLED=false",
		"SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=true",
		"SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=true",
	}
	for _, name := range envFiles {
		assertFileContainsAll(t, filepath.Join(repoRoot, "deploy", name), envExpected)
	}
}

func assertFileContainsAll(t *testing.T, path string, expected []string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, value := range expected {
		if !strings.Contains(string(content), value) {
			t.Errorf("%s does not contain %q", path, value)
		}
	}
}
