package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	_ "modernc.org/sqlite"
)

var opsAdvisoryLockTestDBSequence atomic.Uint64

func openOpsAdvisoryLockSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:ops_advisory_lock_test_%d?mode=memory&cache=shared", opsAdvisoryLockTestDBSequence.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("ping sqlite: %v; close sqlite: %v", err, closeErr)
		}
		t.Fatalf("ping sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	return db
}

func TestTryAcquireDBAdvisoryLockSQLiteContract(t *testing.T) {
	db := openOpsAdvisoryLockSQLiteDB(t)
	lockID := hashAdvisoryLockID(t.Name())

	release, ok := tryAcquireDBAdvisoryLock(context.Background(), db, lockID)
	if !ok || release == nil {
		t.Fatal("first SQLite advisory lock acquisition failed")
	}

	if secondRelease, ok := tryAcquireDBAdvisoryLock(context.Background(), db, lockID); ok {
		secondRelease()
		t.Fatal("same SQLite advisory lock key acquired concurrently")
	}

	otherRelease, ok := tryAcquireDBAdvisoryLock(context.Background(), db, lockID+1)
	if !ok || otherRelease == nil {
		t.Fatal("different SQLite advisory lock key should acquire independently")
	}
	otherRelease()

	release()
	release()

	reacquiredRelease, ok := tryAcquireDBAdvisoryLock(context.Background(), db, lockID)
	if !ok || reacquiredRelease == nil {
		t.Fatal("SQLite advisory lock was not reacquirable after release")
	}
	reacquiredRelease()
}

func TestTryAcquireDBAdvisoryLockSQLiteHonorsCanceledContext(t *testing.T) {
	db := openOpsAdvisoryLockSQLiteDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if release, ok := tryAcquireDBAdvisoryLock(ctx, db, hashAdvisoryLockID(t.Name())); ok || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("advisory lock acquired with a canceled context")
	}
}

func TestTryAcquireDBAdvisoryLockPreservesMySQLBehavior(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close sqlmock database: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("mysql expectations: %v", err)
		}
	})

	const lockID int64 = 42
	const lockName = "ikik_api_ops_42"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT sqlite_version()")).
		WillReturnError(errors.New("FUNCTION sqlite_version does not exist"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 0).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectClose()

	release, ok := tryAcquireDBAdvisoryLock(context.Background(), db, lockID)
	if !ok || release == nil {
		t.Fatal("mysql advisory lock acquisition failed")
	}
	release()
	release()

}
