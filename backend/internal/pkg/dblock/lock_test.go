package dblock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	_ "modernc.org/sqlite"
)

var sqliteLockTestDBSequence atomic.Uint64

func openSQLiteLockTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:dblock_lock_test_%d?mode=memory&cache=shared", sqliteLockTestDBSequence.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAcquireNamedLockSQLiteSameKeyMutualExclusion(t *testing.T) {
	db := openSQLiteLockTestDB(t)
	key := t.Name()

	releaseFirst, err := AcquireNamedLock(context.Background(), db, key, 0)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer releaseFirst()

	if unexpectedRelease, err := AcquireNamedLock(context.Background(), db, key, 0); err == nil {
		unexpectedRelease()
		t.Fatal("same-key try-lock unexpectedly succeeded")
	}

	started := make(chan struct{})
	acquired := make(chan func(), 1)
	errCh := make(chan error, 1)
	go func() {
		close(started)
		release, err := AcquireNamedLock(context.Background(), db, key, 2)
		if err != nil {
			errCh <- err
			return
		}
		acquired <- release
	}()

	<-started
	select {
	case release := <-acquired:
		release()
		t.Fatal("same-key waiter acquired before the holder released")
	case err := <-errCh:
		t.Fatalf("same-key waiter failed before release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	select {
	case release := <-acquired:
		release()
	case err := <-errCh:
		t.Fatalf("same-key waiter failed after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("same-key waiter did not acquire after release")
	}
}

func TestAcquireNamedLockSQLiteDifferentKeysProceedInParallel(t *testing.T) {
	db := openSQLiteLockTestDB(t)

	releaseFirst, err := AcquireNamedLock(context.Background(), db, t.Name()+"/first", 0)
	if err != nil {
		t.Fatalf("acquire first key: %v", err)
	}
	defer releaseFirst()

	result := make(chan error, 1)
	go func() {
		release, err := AcquireNamedLock(context.Background(), db, t.Name()+"/second", 1)
		if err == nil {
			release()
		}
		result <- err
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("different key should acquire while first key is held: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("different key was blocked by unrelated lock")
	}
}

func TestAcquireNamedLockSQLiteHonorsCancellationAndTimeout(t *testing.T) {
	db := openSQLiteLockTestDB(t)
	key := t.Name()

	release, err := AcquireNamedLock(context.Background(), db, key, 0)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcquireNamedLock(ctx, db, key, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire error = %v, want context.Canceled", err)
	}

	startedAt := time.Now()
	if _, err := AcquireNamedLock(context.Background(), db, key, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed acquire error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("timeout elapsed = %v, want approximately one second", elapsed)
	}
}

func TestAcquireNamedLockSQLiteReleaseIsIdempotentAndWorksDuringPanic(t *testing.T) {
	db := openSQLiteLockTestDB(t)
	key := t.Name()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		release, err := AcquireNamedLock(context.Background(), db, key, 0)
		if err != nil {
			t.Fatalf("acquire before panic: %v", err)
		}
		defer release()
		panic("test panic")
	}()

	release, err := AcquireNamedLock(context.Background(), db, key, 0)
	if err != nil {
		t.Fatalf("reacquire after panic: %v", err)
	}
	release()
	release()

	releaseAgain, err := AcquireNamedLock(context.Background(), db, key, 0)
	if err != nil {
		t.Fatalf("reacquire after idempotent release: %v", err)
	}
	releaseAgain()
}

func TestAcquireNamedLockPreservesMySQLNamedLockBehavior(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock db: %v", err)
	}
	t.Cleanup(func() {
		// Closing sqlmock without an ExpectClose reports a mock expectation error; this test validates only lock SQL.
		_ = db.Close()
	})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT sqlite_version()")).
		WillReturnError(errors.New("FUNCTION sqlite_version does not exist"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs("test-lock", 7).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs("test-lock").
		WillReturnResult(sqlmock.NewResult(0, 1))

	release, err := AcquireNamedLock(context.Background(), db, "test-lock", 7)
	if err != nil {
		t.Fatalf("acquire mysql lock: %v", err)
	}
	release()
	release()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mysql expectations: %v", err)
	}
}
