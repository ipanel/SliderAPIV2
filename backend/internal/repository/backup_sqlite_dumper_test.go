package repository

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"ikik-api/internal/config"

	_ "modernc.org/sqlite"
)

func TestProvideDBDumperSelectsDriver(t *testing.T) {
	mysqlDumper, err := ProvideDBDumper(&config.Config{Database: config.DatabaseConfig{Driver: config.DatabaseDriverMySQL}}, nil)
	if err != nil {
		t.Fatalf("ProvideDBDumper(mysql): %v", err)
	}
	if _, ok := mysqlDumper.(*MySQLDumper); !ok {
		t.Fatalf("ProvideDBDumper(mysql) returned %T", mysqlDumper)
	}

	db := openSQLiteBackupTestDB(t, filepath.Join(t.TempDir(), "provider.db"))
	sqliteDumper, err := ProvideDBDumper(&config.Config{Database: config.DatabaseConfig{Driver: config.DatabaseDriverSQLite}}, db)
	if err != nil {
		t.Fatalf("ProvideDBDumper(sqlite): %v", err)
	}
	if _, ok := sqliteDumper.(*SQLiteDumper); !ok {
		t.Fatalf("ProvideDBDumper(sqlite) returned %T", sqliteDumper)
	}
}

func TestSQLiteDumperDumpRestoreWALDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db := openSQLiteBackupTestDB(t, path)
	db.SetMaxOpenConns(4)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatalf("disable WAL autocheckpoint: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO items(value) VALUES ('before')`); err != nil {
		t.Fatalf("insert initial row: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("expected active WAL file, stat err=%v", err)
	}

	dumper, err := NewSQLiteDumper(db)
	if err != nil {
		t.Fatal(err)
	}
	backupReader, err := dumper.Dump(ctx)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	backup, err := io.ReadAll(backupReader)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if err := backupReader.Close(); err != nil {
		t.Fatalf("close dump: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE items SET value = 'after' WHERE id = 1`); err != nil {
		t.Fatalf("modify row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO items(value) VALUES ('new')`); err != nil {
		t.Fatalf("insert new row: %v", err)
	}

	if err := dumper.Restore(ctx, bytes.NewReader(backup)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var count int
	var value string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(value) FROM items`).Scan(&count, &value); err != nil {
		t.Fatalf("query restored data: %v", err)
	}
	if count != 1 || value != "before" {
		t.Fatalf("restored data = count %d, value %q; want 1, before", count, value)
	}
}

func TestSQLiteDumperSupportsSharedMemoryDatabase(t *testing.T) {
	const dsn = "file:sqlite-backup-memory?mode=memory&cache=shared"
	db := openSQLiteBackupTestDB(t, dsn)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES ('mode', 'before')`); err != nil {
		t.Fatalf("insert setting: %v", err)
	}

	dumper, err := NewSQLiteDumper(db)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := dumper.Dump(ctx)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	backup, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close dump: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE settings SET value = 'after' WHERE key = 'mode'`); err != nil {
		t.Fatalf("modify setting: %v", err)
	}
	if err := dumper.Restore(ctx, bytes.NewReader(backup)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'mode'`).Scan(&value); err != nil {
		t.Fatalf("query restored setting: %v", err)
	}
	if value != "before" {
		t.Fatalf("restored value = %q; want before", value)
	}
}

func openSQLiteBackupTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := path
	if path != ":memory:" && len(path) >= 5 && path[:5] != "file:" {
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping sqlite: %v", err)
	}
	return db
}
