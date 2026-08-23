package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDataDirPrefersExplicitDirectory(t *testing.T) {
	explicit := t.TempDir()
	working := t.TempDir()
	executable := t.TempDir()

	got := resolveDataDir(explicit, "", working, executable)
	if got != explicit {
		t.Fatalf("resolveDataDir() = %q, want explicit directory %q", got, explicit)
	}
}

func TestResolveDataDirMakesExplicitDirectoryAbsolute(t *testing.T) {
	working := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	got := resolveDataDir("data", "", working, t.TempDir())
	want := filepath.Join(working, "data")
	if got != want {
		t.Fatalf("resolveDataDir() = %q, want absolute explicit directory %q", got, want)
	}
}

func TestResolveDataDirPrefersExecutableInstallOverWorkingDirectory(t *testing.T) {
	working := t.TempDir()
	executable := t.TempDir()
	writeDataDirMarker(t, working, "config.yaml")
	writeDataDirMarker(t, executable, ".installed")

	got := resolveDataDir("", "", working, executable)
	if got != executable {
		t.Fatalf("resolveDataDir() = %q, want executable directory %q", got, executable)
	}
}

func TestResolveDataDirUsesWorkingInstallWhenExecutableHasNoState(t *testing.T) {
	working := t.TempDir()
	executable := t.TempDir()
	writeDataDirMarker(t, working, "config.yaml")

	got := resolveDataDir("", "", working, executable)
	if got != working {
		t.Fatalf("resolveDataDir() = %q, want working directory %q", got, working)
	}
}

func TestResolveDataDirUsesLegacyConfigDirectory(t *testing.T) {
	working := t.TempDir()
	executable := t.TempDir()
	legacy := t.TempDir()
	writeDataDirMarker(t, legacy, "config.yaml")

	got := resolveDataDir("", "", working, executable, legacy)
	if got != legacy {
		t.Fatalf("resolveDataDir() = %q, want legacy config directory %q", got, legacy)
	}
}

func TestResolveDataDirPrefersDockerDirectoryWithInstallState(t *testing.T) {
	docker := t.TempDir()
	working := t.TempDir()
	executable := t.TempDir()
	writeDataDirMarker(t, docker, ".installed")
	writeDataDirMarker(t, executable, "config.yaml")

	got := resolveDataDir("", docker, working, executable)
	if got != docker {
		t.Fatalf("resolveDataDir() = %q, want Docker data directory %q", got, docker)
	}
}

func TestResolveDataDirUsesWritableDockerDirectoryForFreshInstall(t *testing.T) {
	docker := t.TempDir()
	working := t.TempDir()
	executable := t.TempDir()

	got := resolveDataDir("", docker, working, executable)
	if got != docker {
		t.Fatalf("resolveDataDir() = %q, want Docker data directory %q", got, docker)
	}
}

func TestResolveDataDirUsesWorkingDirectoryForFreshInstall(t *testing.T) {
	working := t.TempDir()
	executable := t.TempDir()

	got := resolveDataDir("", "", working, executable)
	if got != working {
		t.Fatalf("resolveDataDir() = %q, want working directory %q", got, working)
	}
}

func TestDockerDataDirForOSSkipsUnixPathOnWindows(t *testing.T) {
	if got := dockerDataDirForOS("windows"); got != "" {
		t.Fatalf("dockerDataDirForOS(windows) = %q, want empty", got)
	}
	if got := dockerDataDirForOS("linux"); got != runtimeDockerDataDir {
		t.Fatalf("dockerDataDirForOS(linux) = %q, want %q", got, runtimeDockerDataDir)
	}
}

func TestSameDirectoryUsesPlatformCaseRules(t *testing.T) {
	parent := t.TempDir()
	left := filepath.Join(parent, "Data")
	right := filepath.Join(parent, "data")

	got := sameDirectory(left, right)
	want := runtime.GOOS == "windows"
	if got != want {
		t.Fatalf("sameDirectory(%q, %q) = %t, want %t on %s", left, right, got, want, runtime.GOOS)
	}
}

func TestResolveDatabasePathUsesDataDirForSQLite(t *testing.T) {
	dataDir := t.TempDir()
	got := resolveDatabasePath(DatabaseDriverSQLite, "ikik-api.db", dataDir)
	want := filepath.Join(dataDir, "ikik-api.db")
	if got != want {
		t.Fatalf("resolveDatabasePath() = %q, want %q", got, want)
	}
}

func TestResolveDatabasePathLeavesAbsoluteAndNonSQLitePathsUnchanged(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "custom.db")
	if got := resolveDatabasePath(DatabaseDriverSQLite, absolute, t.TempDir()); got != absolute {
		t.Fatalf("absolute SQLite path changed to %q", got)
	}
	if got := resolveDatabasePath(DatabaseDriverMySQL, " ignored ", t.TempDir()); got != "ignored" {
		t.Fatalf("non-SQLite path = %q, want trimmed value", got)
	}
}

func TestGetServerAddressUsesResolvedDataDir(t *testing.T) {
	dataDir := t.TempDir()
	configContents := []byte("server:\n  host: 127.0.0.1\n  port: 19091\n")
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), configContents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("SERVER_HOST", "")
	t.Setenv("SERVER_PORT", "")

	if got := GetServerAddress(); got != "127.0.0.1:19091" {
		t.Fatalf("GetServerAddress() = %q, want 127.0.0.1:19091", got)
	}
}

func writeDataDirMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}
