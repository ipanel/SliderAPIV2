package service

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"

	"ikik-api/internal/pkg/dblock"
)

func hashAdvisoryLockID(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

func tryAcquireDBAdvisoryLock(ctx context.Context, db *sql.DB, lockID int64) (func(), bool) {
	release, err := dblock.AcquireNamedLock(ctx, db, fmt.Sprintf("ikik_api_ops_%d", lockID), 0)
	if err != nil {
		return nil, false
	}
	return release, true
}
