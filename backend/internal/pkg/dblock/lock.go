package dblock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

var sqliteNamedLocks = newLocalNamedLockRegistry()

type localNamedLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*localNamedLockEntry
}

type localNamedLockEntry struct {
	token chan struct{}
	refs  int
}

func newLocalNamedLockRegistry() *localNamedLockRegistry {
	return &localNamedLockRegistry{locks: make(map[string]*localNamedLockEntry)}
}

func (r *localNamedLockRegistry) retain(name string) *localNamedLockEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.locks[name]
	if entry == nil {
		entry = &localNamedLockEntry{token: make(chan struct{}, 1)}
		r.locks[name] = entry
	}
	entry.refs++
	return entry
}

func (r *localNamedLockRegistry) releaseRef(name string, entry *localNamedLockEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry.refs--
	if entry.refs == 0 && r.locks[name] == entry {
		delete(r.locks, name)
	}
}

func (r *localNamedLockRegistry) acquire(ctx context.Context, name string, timeoutSeconds int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry := r.retain(name)
	acquired := false
	defer func() {
		if !acquired {
			r.releaseRef(name, entry)
		}
	}()

	if timeoutSeconds <= 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case entry.token <- struct{}{}:
			acquired = true
		default:
			return nil, fmt.Errorf("failed to acquire sqlite named lock %q", name)
		}
	} else {
		timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("timed out acquiring sqlite named lock %q: %w", name, context.DeadlineExceeded)
		case entry.token <- struct{}{}:
			acquired = true
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-entry.token
			r.releaseRef(name, entry)
		})
	}, nil
}

// AcquireNamedLock acquires a named lock and returns an idempotent release
// function. MySQL/MariaDB locks use GET_LOCK on a dedicated connection.
// SQLite locks are process-local and keyed by name. timeoutSeconds <= 0 means
// try once without waiting.
func AcquireNamedLock(ctx context.Context, db *sql.DB, name string, timeoutSeconds int) (func(), error) {
	if db == nil {
		return nil, errors.New("nil sql db")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}

	var sqliteVersion string
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sqliteVersion); err == nil && sqliteVersion != "" {
		_ = conn.Close()
		return sqliteNamedLocks.acquire(ctx, name, timeoutSeconds)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	var acquired int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, timeoutSeconds).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if acquired != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to acquire mysql named lock %q", name)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(unlockCtx, "SELECT RELEASE_LOCK(?)", name)
			_ = conn.Close()
		})
	}
	return release, nil
}
