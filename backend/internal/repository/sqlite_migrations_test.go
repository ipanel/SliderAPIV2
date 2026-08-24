package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/require"
	"ikik-api/migrations"
)

func TestApplySQLiteMigrationsCreatesCurrentSchema(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	require.NoError(t, InitializeDatabaseSchema(ctx, db, dialect.SQLite))

	for _, table := range []string{
		"scheduler_outbox",
		"scheduled_test_plans",
		"scheduled_test_results",
		"usage_billing_dedup",
		"usage_billing_dedup_archive",
		"ops_error_logs",
		"usage_dashboard_hourly",
		"billing_usage_entries",
		"channels",
		"group_rate_schedules",
		"carpool_pools",
		"content_moderation_logs",
	} {
		requireSQLiteObjectExists(t, db, "table", table, true)
	}

	for table, columns := range map[string][]string{
		"scheduled_test_plans":    {"auto_recover"},
		"ops_error_logs":          {"inbound_endpoint", "requested_model"},
		"carpool_members":         {"quota_share_ratio"},
		"content_moderation_logs": {"matched_keyword"},
	} {
		for _, column := range columns {
			requireSQLiteColumnExists(t, db, table, column)
		}
	}

	requireSQLiteObjectExists(t, db, "table", "sora_accounts", false)
	requireSQLiteObjectExists(t, db, "table", "sora_generations", false)

	var migrationCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount))
	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	expected := 0
	for _, name := range files {
		content, readErr := fs.ReadFile(migrations.FS, name)
		require.NoError(t, readErr)
		if strings.TrimSpace(string(content)) != "" {
			expected++
		}
	}
	require.Equal(t, expected, migrationCount)

	require.NoError(t, ApplySQLiteMigrations(ctx, db), "migration projection must be idempotent")
	var reappliedCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&reappliedCount))
	require.Equal(t, migrationCount, reappliedCount)

	var quickCheck string
	require.NoError(t, db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck))
	require.Equal(t, "ok", quickCheck)

	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rows.Close()) })
	require.False(t, rows.Next(), "foreign_key_check returned violations")
	require.NoError(t, rows.Err())
}

func TestApplySQLiteMigrationsAppliesCanonicalSeeds(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	require.NoError(t, applySQLiteMigrationsFS(ctx, db, sqliteCanonicalSeedTestFS(t)))

	var groupName, groupDescription, supportedModelScopes string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT name, description, supported_model_scopes
		FROM groups
		WHERE name = ?`, "default",
	).Scan(&groupName, &groupDescription, &supportedModelScopes))
	require.Equal(t, "default", groupName)
	require.Equal(t, "Default group", groupDescription)
	require.JSONEq(t, `["claude", "gemini_text", "gemini_image"]`, supportedModelScopes)

	type expectedAlertRule struct {
		metricType       string
		operator         string
		threshold        float64
		windowMinutes    int
		sustainedMinutes int
		severity         string
		cooldownMinutes  int
	}
	expectedRules := []expectedAlertRule{
		{"error_rate", ">", 5, 5, 5, "P1", 20},
		{"success_rate", "<", 95, 5, 5, "P0", 15},
		{"p99_latency_ms", ">", 3000, 5, 10, "P2", 30},
		{"p95_latency_ms", ">", 2000, 5, 10, "P2", 30},
		{"cpu_usage_percent", ">", 85, 5, 10, "P2", 30},
		{"memory_usage_percent", ">", 90, 5, 10, "P1", 20},
		{"concurrency_queue_depth", ">", 100, 5, 5, "P1", 20},
		{"error_rate", ">", 20, 1, 1, "P0", 15},
	}
	rows, err := db.QueryContext(ctx, `
		SELECT name, description, metric_type, operator, threshold,
		       window_minutes, sustained_minutes, severity, cooldown_minutes,
		       enabled, notify_email
		FROM ops_alert_rules
		ORDER BY id`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rows.Close()) })
	for _, expected := range expectedRules {
		require.True(t, rows.Next())
		var name, description string
		var actual expectedAlertRule
		var enabled, notifyEmail bool
		require.NoError(t, rows.Scan(
			&name, &description, &actual.metricType, &actual.operator, &actual.threshold,
			&actual.windowMinutes, &actual.sustainedMinutes, &actual.severity, &actual.cooldownMinutes,
			&enabled, &notifyEmail,
		))
		require.NotEmpty(t, name)
		require.NotEmpty(t, description)
		require.Equal(t, expected, actual)
		require.True(t, enabled)
		require.True(t, notifyEmail)
	}
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())

	var templateName, provider, description, extraHeaders, bodyOverrideMode, bodyOverride string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT name, provider, description, extra_headers, body_override_mode, body_override
		FROM channel_monitor_request_templates
		WHERE provider = ?`, "anthropic",
	).Scan(&templateName, &provider, &description, &extraHeaders, &bodyOverrideMode, &bodyOverride))
	require.NotEmpty(t, templateName)
	require.Equal(t, "anthropic", provider)
	require.Contains(t, description, "Claude Code 2.1.114")
	require.Equal(t, "merge", bodyOverrideMode)
	require.JSONEq(t, `{
		"User-Agent": "claude-cli/2.1.114 (external, sdk-cli)",
		"X-App": "cli",
		"anthropic-version": "2023-06-01",
		"anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01",
		"anthropic-dangerous-direct-browser-access": "true"
	}`, extraHeaders)
	require.JSONEq(t, `{
		"system": [{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}],
		"metadata": {
			"user_id": "user_0000000000000000000000000000000000000000000000000000000000000000_account_00000000-0000-0000-0000-000000000000_session_00000000-0000-0000-0000-000000000000"
		}
	}`, bodyOverride)
}

func TestApplySQLiteMigrationsCanonicalSeedsAreIdempotent(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	fsys := sqliteCanonicalSeedTestFS(t)

	require.NoError(t, applySQLiteMigrationsFS(ctx, db, fsys))
	require.NoError(t, applySQLiteMigrationsFS(ctx, db, fsys))

	for table, expected := range map[string]int{
		"groups":                            1,
		"ops_alert_rules":                   8,
		"channel_monitor_request_templates": 1,
	} {
		var count int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteSQLiteIdentifier(table)).Scan(&count))
		require.Equalf(t, expected, count, "unexpected row count in %s", table)
	}
}

func TestApplySQLiteMigrationsCanonicalSeedsPreserveUserChanges(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	fsys := sqliteCanonicalSeedTestFS(t)
	require.NoError(t, applySQLiteMigrationsFS(ctx, db, fsys))

	_, err := db.ExecContext(ctx, "UPDATE groups SET description = ? WHERE name = ?", "user group", "default")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE ops_alert_rules SET threshold = ?, description = ? WHERE metric_type = ? AND threshold = ?", 12.5, "user alert", "error_rate", 5)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		UPDATE channel_monitor_request_templates
		SET description = ?, body_override_mode = ?
		WHERE provider = ?`, "user template", "replace", "anthropic")
	require.NoError(t, err)

	require.NoError(t, applySQLiteMigrationsFS(ctx, db, fsys))

	var groupDescription string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT description FROM groups WHERE name = ?", "default").Scan(&groupDescription))
	require.Equal(t, "user group", groupDescription)

	var threshold float64
	var alertDescription string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT threshold, description FROM ops_alert_rules WHERE metric_type = ? AND threshold = ?", "error_rate", 12.5,
	).Scan(&threshold, &alertDescription))
	require.Equal(t, 12.5, threshold)
	require.Equal(t, "user alert", alertDescription)

	var templateDescription, bodyOverrideMode string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT description, body_override_mode
		FROM channel_monitor_request_templates
		WHERE provider = ?`, "anthropic",
	).Scan(&templateDescription, &bodyOverrideMode))
	require.Equal(t, "user template", templateDescription)
	require.Equal(t, "replace", bodyOverrideMode)
}

func TestApplySQLiteMigrationsSkipsHistoricalDML(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	require.NoError(t, execSQLiteStatements(ctx, db,
		"CREATE TABLE historical_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL)",
		"INSERT INTO historical_rows (id, value) VALUES (1, 'user value')",
	))

	fsys := fstest.MapFS{
		"200_historical_backfill.sql": {Data: []byte(`
			UPDATE historical_rows SET value = 'backfilled' WHERE id = 1;
			DELETE FROM historical_rows WHERE id = 1;
			INSERT INTO historical_rows (id, value) VALUES (2, 'historical insert');
			TRUNCATE TABLE historical_rows;
		`)},
	}
	require.NoError(t, applySQLiteMigrationsFS(ctx, db, fsys))

	rows, err := db.QueryContext(ctx, "SELECT id, value FROM historical_rows ORDER BY id")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rows.Close()) })
	require.True(t, rows.Next())
	var id int
	var value string
	require.NoError(t, rows.Scan(&id, &value))
	require.Equal(t, 1, id)
	require.Equal(t, "user value", value)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}

func sqliteCanonicalSeedTestFS(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{
		"001_seed_test_schema.sql": {Data: []byte(`
			CREATE TABLE groups (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				description TEXT,
				supported_model_scopes JSON NOT NULL,
				messages_dispatch_model_config JSON NOT NULL,
				models_list_config JSON NOT NULL,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE TABLE channel_monitor_request_templates (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				provider TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				extra_headers JSON NOT NULL DEFAULT '{}',
				body_override_mode TEXT NOT NULL DEFAULT 'off',
				body_override JSON,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (provider, name)
			);
		`)},
	}
	for _, name := range sqliteCanonicalSeedMigrationNames {
		content, err := fs.ReadFile(migrations.FS, name)
		require.NoError(t, err)
		fsys[name] = &fstest.MapFile{Data: content}
	}
	return fsys
}

func execSQLiteStatements(ctx context.Context, db *sql.DB, statements ...string) error {
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func TestApplySQLiteMigrationsRejectsChecksumMismatch(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	ctx := context.Background()
	first := fstest.MapFS{
		"001_test.sql": {Data: []byte("CREATE TABLE example (id INTEGER PRIMARY KEY);")},
	}
	require.NoError(t, applySQLiteMigrationsFS(ctx, db, first))

	changed := fstest.MapFS{
		"001_test.sql": {Data: []byte("CREATE TABLE example (id INTEGER PRIMARY KEY, name TEXT);")},
	}
	err := applySQLiteMigrationsFS(ctx, db, changed)
	require.ErrorContains(t, err, "checksum mismatch")
}

func requireSQLiteObjectExists(t *testing.T, db *sql.DB, objectType, name string, expected bool) {
	t.Helper()
	var count int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = ? AND name = ?", objectType, name,
	).Scan(&count)
	require.NoError(t, err)
	if expected {
		require.Equalf(t, 1, count, "%s %s should exist", objectType, name)
	} else {
		require.Zero(t, count, "%s %s should not exist", objectType, name)
	}
}

func requireSQLiteColumnExists(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+quoted+")")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		if name == column {
			return
		}
	}
	require.NoError(t, rows.Err())
	t.Fatalf("column %s.%s should exist", table, column)
}
