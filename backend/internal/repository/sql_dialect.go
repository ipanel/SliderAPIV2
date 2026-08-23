package repository

import (
	"context"
	"database/sql"
)

type sqlRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// isSQLiteExecutor detects SQLite without relying on a concrete driver type.
// It is intentionally usable with both *sql.DB and *sql.Tx.
func isSQLiteExecutor(ctx context.Context, queryer sqlRowQuerier) bool {
	if queryer == nil {
		return false
	}
	var version string
	return queryer.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version) == nil && version != ""
}
