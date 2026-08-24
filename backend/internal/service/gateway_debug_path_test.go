package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDebugGatewayBodyPath(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	t.Setenv("DATA_DIR", dataDir)

	t.Run("normal relative path", func(t *testing.T) {
		got, err := resolveDebugGatewayBodyPath(filepath.Join("logs", "gateway.log"))
		if err != nil {
			t.Fatalf("resolve normal relative path: %v", err)
		}
		want := filepath.Join(dataDir, "logs", "gateway.log")
		if got != want {
			t.Fatalf("resolved path = %q, want %q", got, want)
		}
	})

	t.Run("parent traversal", func(t *testing.T) {
		if _, err := resolveDebugGatewayBodyPath(filepath.Join("..", "gateway.log")); err == nil {
			t.Fatal("expected parent traversal to be rejected")
		}
	})

	t.Run("absolute path outside data directory", func(t *testing.T) {
		outside := filepath.Join(root, "outside.log")
		if _, err := resolveDebugGatewayBodyPath(outside); err == nil {
			t.Fatal("expected absolute path outside data directory to be rejected")
		}
	})
}

func TestResolveDebugGatewayBodyPathRejectsParentSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	outsideDir := filepath.Join(root, "outside")
	for _, dir := range []string{dataDir, outsideDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create directory %q: %v", dir, err)
		}
	}
	t.Setenv("DATA_DIR", dataDir)

	linkDir := filepath.Join(dataDir, "linked")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("symlink creation is unavailable on this platform: %v", err)
	}

	if _, err := resolveDebugGatewayBodyPath(filepath.Join("linked", "gateway.log")); err == nil {
		t.Fatal("expected path through parent symlink outside data directory to be rejected")
	}
}

func TestResolveDebugGatewayBodyPathRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	t.Setenv("DATA_DIR", dataDir)

	outsideFile := filepath.Join(root, "outside.log")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("create outside file: %v", err)
	}
	linkFile := filepath.Join(dataDir, "gateway.log")
	if err := os.Symlink(outsideFile, linkFile); err != nil {
		t.Skipf("symlink creation is unavailable on this platform: %v", err)
	}

	if _, err := resolveDebugGatewayBodyPath("gateway.log"); err == nil {
		t.Fatal("expected final file symlink to be rejected")
	}
}
