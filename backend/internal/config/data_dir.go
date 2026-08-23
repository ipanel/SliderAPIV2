package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	runtimeConfigFileName  = "config.yaml"
	runtimeInstallLockName = ".installed"
	runtimeDockerDataDir   = "/app/data"
)

// ResolveDataDir returns the directory that owns the runtime configuration and
// installation state. Explicit DATA_DIR and Docker storage retain priority.
// For portable binaries, an installed data directory beside the executable is
// preferred over an unrelated current working directory.
func ResolveDataDir() string {
	explicit := strings.TrimSpace(os.Getenv("DATA_DIR"))
	workingDir, err := os.Getwd()
	if err != nil {
		workingDir = "."
	}
	executableDir := ""
	if executablePath, executableErr := os.Executable(); executableErr == nil {
		executableDir = filepath.Dir(executablePath)
	}
	legacyDirs := []string{filepath.Join(workingDir, "config")}
	if runtime.GOOS != "windows" {
		legacyDirs = append(legacyDirs, "/etc/ikik-api")
	}
	return resolveDataDir(explicit, dockerDataDirForOS(runtime.GOOS), workingDir, executableDir, legacyDirs...)
}

// ConfigureDataDir pins the resolved runtime directory in DATA_DIR so setup,
// configuration loading, logging, SQLite, and other components use one path.
func ConfigureDataDir() (string, error) {
	resolved := ResolveDataDir()
	if err := os.Setenv("DATA_DIR", resolved); err != nil {
		return "", fmt.Errorf("set DATA_DIR: %w", err)
	}
	return resolved, nil
}

func resolveDataDir(explicitDataDir, dockerDataDir, workingDir, executableDir string, legacyDirs ...string) string {
	if explicit := cleanDataDir(explicitDataDir); explicit != "" {
		return explicit
	}

	dockerDataDir = cleanDataDir(dockerDataDir)
	if hasInstallState(dockerDataDir) {
		return dockerDataDir
	}

	workingDir = cleanDataDir(workingDir)
	executableDir = cleanDataDir(executableDir)
	if executableDir != "" && !sameDirectory(workingDir, executableDir) && hasInstallState(executableDir) {
		return executableDir
	}
	if hasInstallState(workingDir) {
		return workingDir
	}

	for _, legacyDir := range legacyDirs {
		legacyDir = cleanDataDir(legacyDir)
		if legacyDir != "" && hasInstallState(legacyDir) {
			return legacyDir
		}
	}

	if isWritableDirectory(dockerDataDir) {
		return dockerDataDir
	}
	if workingDir != "" {
		return workingDir
	}
	if executableDir != "" {
		return executableDir
	}
	return "."
}

func cleanDataDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(dir)
}

func hasInstallState(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range []string{runtimeConfigFileName, runtimeInstallLockName} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func isWritableDirectory(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	probe, err := os.CreateTemp(dir, ".ikik-api-write-test-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

func resolveDatabasePath(driver, path, dataDir string) string {
	path = strings.TrimSpace(path)
	if strings.ToLower(strings.TrimSpace(driver)) != DatabaseDriverSQLite {
		return path
	}
	if path == "" {
		path = "ikik-api.db"
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(dataDir, path))
}

func dockerDataDirForOS(goos string) string {
	if goos == "windows" {
		return ""
	}
	return runtimeDockerDataDir
}

func sameDirectory(left, right string) bool {
	if left == "" || right == "" {
		return false
	}

	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
