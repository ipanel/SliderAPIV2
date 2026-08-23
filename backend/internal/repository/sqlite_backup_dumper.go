package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"modernc.org/sqlite"
)

const sqliteBackupPagesPerStep = 256

// SQLiteDumper implements service.DBDumper with SQLite's online backup API.
// It intentionally operates through the application's existing *sql.DB instead
// of copying or replacing the configured database path: file-backed databases
// may have uncheckpointed WAL data, and in-memory databases have no file to
// replace.
type SQLiteDumper struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteDumper creates a dumper for the application's active SQLite pool.
func NewSQLiteDumper(db *sql.DB) (*SQLiteDumper, error) {
	if db == nil {
		return nil, errors.New("nil sqlite database")
	}
	return &SQLiteDumper{db: db}, nil
}

// Dump creates a consistent standalone SQLite database and returns it as a
// stream. The temporary file is removed when the caller closes the stream.
func (d *SQLiteDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	artifact, err := newSQLiteBackupArtifact()
	if err != nil {
		return nil, err
	}

	if err := d.copyDatabase(ctx, artifact.path, false); err != nil {
		artifact.cleanup()
		return nil, fmt.Errorf("backup sqlite database: %w", err)
	}

	file, err := os.Open(artifact.path)
	if err != nil {
		artifact.cleanup()
		return nil, fmt.Errorf("open sqlite backup: %w", err)
	}

	return &removeOnCloseReadCloser{
		ReadCloser: file,
		cleanup:    artifact.cleanup,
	}, nil
}

// Restore validates a streamed SQLite backup in a temporary database and then
// restores it into the active database with SQLite's online backup API. This
// avoids replacing the main database file while the application still owns
// open connections, and lets SQLite coordinate WAL/SHM state itself.
func (d *SQLiteDumper) Restore(ctx context.Context, data io.Reader) error {
	if data == nil {
		return errors.New("nil sqlite backup reader")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	artifact, err := newSQLiteBackupArtifact()
	if err != nil {
		return err
	}
	defer artifact.cleanup()

	file, err := os.OpenFile(artifact.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create sqlite restore file: %w", err)
	}

	written, copyErr := io.Copy(file, &contextReader{ctx: ctx, reader: data})
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write sqlite restore file: %w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync sqlite restore file: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sqlite restore file: %w", closeErr)
	}
	if written == 0 {
		return errors.New("sqlite restore input is empty")
	}

	if err := validateSQLiteBackup(ctx, artifact.path); err != nil {
		return fmt.Errorf("validate sqlite backup: %w", err)
	}
	if err := d.copyDatabase(ctx, artifact.path, true); err != nil {
		return fmt.Errorf("restore sqlite database: %w", err)
	}
	return nil
}

type sqliteBackupDriverConn interface {
	NewBackup(string) (*sqlite.Backup, error)
	NewRestore(string) (*sqlite.Backup, error)
}

func (d *SQLiteDumper) copyDatabase(ctx context.Context, otherPath string, restore bool) error {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) error {
		backupConn, ok := driverConn.(sqliteBackupDriverConn)
		if !ok {
			return fmt.Errorf("sqlite driver connection %T does not support online backup", driverConn)
		}

		var backup *sqlite.Backup
		if restore {
			backup, err = backupConn.NewRestore(sqliteFileDSN(otherPath, true))
		} else {
			backup, err = backupConn.NewBackup(sqliteFileDSN(otherPath, false))
		}
		if err != nil {
			return err
		}

		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()

		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, stepErr := backup.Step(sqliteBackupPagesPerStep)
			if stepErr != nil {
				return stepErr
			}
			if !more {
				finishErr := backup.Finish()
				finished = true
				return finishErr
			}
		}
	})
}

func validateSQLiteBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check returned %q", result)
	}
	return nil
}

func sqliteReadOnlyDSN(path string) string {
	return sqliteFileDSN(path, true)
}

func sqliteFileDSN(path string, readOnly bool) string {
	slashPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}

	u := url.URL{Scheme: "file", Path: slashPath}
	query := url.Values{}
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Set("mode", "rwc")
	}
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	return u.String()
}

type sqliteBackupArtifact struct {
	dir  string
	path string
}

func newSQLiteBackupArtifact() (*sqliteBackupArtifact, error) {
	dir, err := os.MkdirTemp("", "ikik-sqlite-backup-*")
	if err != nil {
		return nil, fmt.Errorf("create sqlite backup directory: %w", err)
	}
	return &sqliteBackupArtifact{
		dir:  dir,
		path: filepath.Join(dir, "backup.sqlite"),
	}, nil
}

func (a *sqliteBackupArtifact) cleanup() {
	_ = os.RemoveAll(a.dir)
}

type removeOnCloseReadCloser struct {
	io.ReadCloser
	cleanup func()
	once    sync.Once
}

func (r *removeOnCloseReadCloser) Close() error {
	closeErr := r.ReadCloser.Close()
	r.once.Do(r.cleanup)
	return closeErr
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
