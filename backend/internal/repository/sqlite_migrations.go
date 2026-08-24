package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"ikik-api/migrations"
)

const sqliteSchemaMigrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	checksum   TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

const sqliteDefaultGroupCanonicalSeedSQL = `
INSERT INTO groups (
	name, description, supported_model_scopes, messages_dispatch_model_config, models_list_config, created_at, updated_at
)
SELECT
	'default', 'Default group', '["claude", "gemini_text", "gemini_image"]', '{}', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
WHERE NOT EXISTS (SELECT 1 FROM groups);`

var (
	sqliteCreateTableRE = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[` + "`" + `]?([A-Za-z0-9_]+)[` + "`" + `]?`)
	sqliteDropTableRE   = regexp.MustCompile(`(?is)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?[` + "`" + `]?([A-Za-z0-9_]+)[` + "`" + `]?`)
	sqliteAlterTableRE  = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+[` + "`" + `]?([A-Za-z0-9_]+)[` + "`" + `]?\s+(.+)$`)
	sqliteCreateIndexRE = regexp.MustCompile(`(?is)^CREATE\s+(?:UNIQUE\s+)?INDEX\b`)
	sqliteDropIndexRE   = regexp.MustCompile(`(?is)^DROP\s+INDEX\b`)

	sqliteAutoPrimaryKeyRE       = regexp.MustCompile(`(?i)\b(?:BIGINT|INT|INTEGER|SMALLINT)\s+AUTO_INCREMENT\s+PRIMARY\s+KEY\b`)
	sqliteAutoIncrementRE        = regexp.MustCompile(`(?i)\s+AUTO_INCREMENT\b`)
	sqliteDateTimePrecisionRE    = regexp.MustCompile(`(?i)\b(DATETIME|TIMESTAMP)\s*\(\s*\d+\s*\)`)
	sqliteCurrentTimestampPrecRE = regexp.MustCompile(`(?i)CURRENT_TIMESTAMP\s*\(\s*\d+\s*\)`)
	sqliteDefaultNowRE           = regexp.MustCompile(`(?i)DEFAULT\s+(?:NOW|UTC_TIMESTAMP)\s*\(\s*(?:\d+\s*)?\)`)
	sqliteIndexConcurrentlyRE    = regexp.MustCompile(`(?i)\s+CONCURRENTLY\b`)
	sqliteDropIndexOnTableRE     = regexp.MustCompile(`(?is)^(DROP\s+INDEX\s+(?:IF\s+EXISTS\s+)?[` + "`" + `]?[A-Za-z0-9_]+[` + "`" + `]?)\s+ON\s+[` + "`" + `]?[A-Za-z0-9_]+[` + "`" + `]?\s*$`)
	sqliteInsertIgnoreRE         = regexp.MustCompile(`(?i)^INSERT\s+IGNORE\s+INTO\b`)
	sqliteNowFunctionRE          = regexp.MustCompile(`(?i)\bNOW\s*\(\s*\)`)
)

var sqliteCanonicalSeedMigrationNames = []string{
	"008_seed_default_group.sql",
	"033_ops_monitoring_vnext.sql",
	"129_seed_claude_code_template.sql",
}

// ApplySQLiteMigrations projects the schema-affecting parts of the versioned
// MySQL migration stream onto SQLite. Ent creates the ORM-owned tables first;
// this runner adds the raw-SQL-only tables, columns, and indexes used by the
// scheduler, operations, billing, settlement, monitoring, shop, and carpool
// subsystems.
func ApplySQLiteMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("nil sql db")
	}
	return applySQLiteMigrationsFS(ctx, db, migrations.FS)
}

func applySQLiteMigrationsFS(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, sqliteSchemaMigrationsTableDDL); err != nil {
		return fmt.Errorf("create sqlite schema_migrations: %w", err)
	}

	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("list sqlite migrations: %w", err)
	}
	sort.Strings(files)
	canonicalSeedContents := make(map[string]string, len(sqliteCanonicalSeedMigrationNames))

	for _, name := range files {
		contentBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read sqlite migration %s: %w", name, err)
		}
		content := strings.TrimSpace(string(contentBytes))
		if content == "" {
			continue
		}
		if isSQLiteCanonicalSeedMigration(name) {
			canonicalSeedContents[name] = content
		}
		sum := sha256.Sum256([]byte(content))
		checksum := hex.EncodeToString(sum[:])

		var existing string
		err = db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE filename = ?", name).Scan(&existing)
		switch {
		case err == nil:
			if existing != checksum && !isMigrationChecksumCompatible(name, existing, checksum) {
				return fmt.Errorf("sqlite migration %s checksum mismatch (db=%s file=%s)", name, existing, checksum)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check sqlite migration %s: %w", name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin sqlite migration %s: %w", name, err)
		}
		if err := applySQLiteMigrationContent(ctx, tx, content); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply sqlite migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (filename, checksum) VALUES (?, ?)", name, checksum); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record sqlite migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit sqlite migration %s: %w", name, err)
		}
	}

	if err := applySQLiteCanonicalSeeds(ctx, db, canonicalSeedContents); err != nil {
		return err
	}

	if err := sqliteIntegrityCheck(ctx, db, "foreign_key_check"); err != nil {
		return err
	}
	return sqliteIntegrityCheck(ctx, db, "quick_check")
}

func applySQLiteMigrationContent(ctx context.Context, tx *sql.Tx, content string) error {
	for _, raw := range splitSQLStatements(content) {
		stmt := strings.TrimSpace(stripSQLLineComment(raw))
		if stmt == "" {
			continue
		}
		switch {
		case sqliteCreateTableRE.MatchString(stmt):
			if _, err := tx.ExecContext(ctx, translateSQLiteDDL(stmt)); err != nil {
				return fmt.Errorf("create table: %w; sql=%s", err, compactSQL(stmt))
			}
		case sqliteDropTableRE.MatchString(stmt):
			translated := strings.TrimSpace(stmt)
			translated = regexp.MustCompile(`(?i)\s+CASCADE\s*$`).ReplaceAllString(translated, "")
			if _, err := tx.ExecContext(ctx, translated); err != nil {
				return fmt.Errorf("drop table: %w; sql=%s", err, compactSQL(stmt))
			}
		case sqliteAlterTableRE.MatchString(stmt):
			if err := applySQLiteAlterTable(ctx, tx, stmt); err != nil {
				return err
			}
		case sqliteCreateIndexRE.MatchString(stmt):
			translated, ok := translateSQLiteCreateIndex(stmt)
			if !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, translated); err != nil {
				// Ent creates the latest table shape before historical migrations are
				// replayed. An old index can therefore reference a column removed by a
				// later migration; skip that historical index and continue to the final
				// schema state.
				if sqliteIgnorableHistoricalIndexError(err) {
					continue
				}
				return fmt.Errorf("create index: %w; sql=%s", err, compactSQL(stmt))
			}
		case sqliteDropIndexRE.MatchString(stmt):
			translated := sqliteDropIndexOnTableRE.ReplaceAllString(strings.TrimSpace(stmt), "$1")
			if _, err := tx.ExecContext(ctx, translated); err != nil {
				return fmt.Errorf("drop index: %w; sql=%s", err, compactSQL(stmt))
			}
			// All non-canonical DML, including historical backfills and cleanup, is
			// intentionally skipped by the SQLite schema projection.
		}
	}
	return nil
}

func isSQLiteCanonicalSeedMigration(name string) bool {
	for _, canonicalName := range sqliteCanonicalSeedMigrationNames {
		if name == canonicalName {
			return true
		}
	}
	return false
}

func applySQLiteCanonicalSeeds(ctx context.Context, db *sql.DB, contents map[string]string) error {
	if len(contents) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite canonical seeds: %w", err)
	}
	for _, name := range sqliteCanonicalSeedMigrationNames {
		content, ok := contents[name]
		if !ok {
			continue
		}
		if name == "008_seed_default_group.sql" {
			if _, err := tx.ExecContext(ctx, sqliteDefaultGroupCanonicalSeedSQL); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply sqlite canonical seed %s: %w", name, err)
			}
			continue
		}
		if err := applySQLiteCanonicalSeedContent(ctx, tx, content); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply sqlite canonical seed %s: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite canonical seeds: %w", err)
	}
	return nil
}

func applySQLiteCanonicalSeedContent(ctx context.Context, tx *sql.Tx, content string) error {
	for _, raw := range splitSQLStatements(content) {
		stmt := strings.TrimSpace(stripSQLLineComment(raw))
		if !strings.HasPrefix(strings.ToUpper(stmt), "INSERT ") {
			continue
		}

		translated := sqliteInsertIgnoreRE.ReplaceAllString(stmt, "INSERT OR IGNORE INTO")
		translated = sqliteNowFunctionRE.ReplaceAllString(translated, "CURRENT_TIMESTAMP")
		if _, err := tx.ExecContext(ctx, translated); err != nil {
			return fmt.Errorf("insert canonical seed: %w; sql=%s", err, compactSQL(stmt))
		}
	}
	return nil
}

func applySQLiteAlterTable(ctx context.Context, tx *sql.Tx, stmt string) error {
	match := sqliteAlterTableRE.FindStringSubmatch(strings.TrimSpace(stmt))
	if len(match) != 3 {
		return nil
	}
	tableName, body := match[1], strings.TrimSpace(match[2])
	for _, action := range splitSQLTopLevelComma(body) {
		action = strings.TrimSpace(action)
		upper := strings.ToUpper(action)
		switch {
		case strings.HasPrefix(upper, "ADD COLUMN "):
			definition := strings.TrimSpace(action[len("ADD COLUMN "):])
			definition = regexp.MustCompile(`(?i)^IF\s+NOT\s+EXISTS\s+`).ReplaceAllString(definition, "")
			columnName := firstSQLIdentifier(definition)
			if columnName == "" {
				continue
			}
			exists, err := sqliteColumnExists(ctx, tx, tableName, columnName)
			if err != nil {
				return fmt.Errorf("check sqlite column %s.%s: %w", tableName, columnName, err)
			}
			if exists {
				continue
			}
			query := "ALTER TABLE " + quoteSQLiteIdentifier(tableName) + " ADD COLUMN " + translateSQLiteDDL(definition)
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("add sqlite column %s.%s: %w; sql=%s", tableName, columnName, err, compactSQL(query))
			}
		case strings.HasPrefix(upper, "DROP COLUMN "):
			columnName := strings.TrimSpace(action[len("DROP COLUMN "):])
			columnName = regexp.MustCompile(`(?i)^IF\s+EXISTS\s+`).ReplaceAllString(columnName, "")
			columnName = firstSQLIdentifier(columnName)
			if columnName == "" {
				continue
			}
			exists, err := sqliteColumnExists(ctx, tx, tableName, columnName)
			if err != nil {
				return fmt.Errorf("check sqlite column %s.%s: %w", tableName, columnName, err)
			}
			if !exists {
				continue
			}
			query := "ALTER TABLE " + quoteSQLiteIdentifier(tableName) + " DROP COLUMN " + quoteSQLiteIdentifier(columnName)
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("drop sqlite column %s.%s: %w", tableName, columnName, err)
			}
		case strings.HasPrefix(upper, "ADD CONSTRAINT "):
			if err := applySQLiteAddedConstraint(ctx, tx, tableName, action); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "ADD UNIQUE "), strings.HasPrefix(upper, "ADD UNIQUE INDEX "), strings.HasPrefix(upper, "ADD UNIQUE KEY "):
			if err := applySQLiteAddedUnique(ctx, tx, tableName, action); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "DROP INDEX "):
			indexName := firstSQLIdentifier(strings.TrimSpace(action[len("DROP INDEX "):]))
			if indexName != "" {
				if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS "+quoteSQLiteIdentifier(indexName)); err != nil {
					return fmt.Errorf("drop sqlite index %s: %w", indexName, err)
				}
			}
			// MODIFY COLUMN and ALTER COLUMN only change type/default/nullability.
			// SQLite uses dynamic typing and cannot alter those properties in place;
			// current fresh schemas already use the latest compatible definitions.
		}
	}
	return nil
}

func applySQLiteAddedConstraint(ctx context.Context, tx *sql.Tx, tableName, action string) error {
	match := regexp.MustCompile(`(?is)^ADD\s+CONSTRAINT\s+[` + "`" + `]?([A-Za-z0-9_]+)[` + "`" + `]?\s+UNIQUE\s*\((.+)\)\s*$`).FindStringSubmatch(action)
	if len(match) != 3 {
		return nil
	}
	query := "CREATE UNIQUE INDEX IF NOT EXISTS " + quoteSQLiteIdentifier(match[1]) + " ON " + quoteSQLiteIdentifier(tableName) + " (" + match[2] + ")"
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("add sqlite unique constraint %s: %w", match[1], err)
	}
	return nil
}

func applySQLiteAddedUnique(ctx context.Context, tx *sql.Tx, tableName, action string) error {
	match := regexp.MustCompile(`(?is)^ADD\s+UNIQUE(?:\s+(?:INDEX|KEY))?\s+[` + "`" + `]?([A-Za-z0-9_]+)[` + "`" + `]?\s*\((.+)\)\s*$`).FindStringSubmatch(action)
	if len(match) != 3 {
		return nil
	}
	query := "CREATE UNIQUE INDEX IF NOT EXISTS " + quoteSQLiteIdentifier(match[1]) + " ON " + quoteSQLiteIdentifier(tableName) + " (" + match[2] + ")"
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("add sqlite unique index %s: %w", match[1], err)
	}
	return nil
}

func translateSQLiteDDL(stmt string) string {
	stmt = sqliteAutoPrimaryKeyRE.ReplaceAllString(stmt, "INTEGER PRIMARY KEY AUTOINCREMENT")
	stmt = sqliteAutoIncrementRE.ReplaceAllString(stmt, "")
	stmt = sqliteDateTimePrecisionRE.ReplaceAllString(stmt, "$1")
	stmt = sqliteCurrentTimestampPrecRE.ReplaceAllString(stmt, "CURRENT_TIMESTAMP")
	stmt = sqliteDefaultNowRE.ReplaceAllString(stmt, "DEFAULT CURRENT_TIMESTAMP")
	return stmt
}

func translateSQLiteCreateIndex(stmt string) (string, bool) {
	upper := strings.ToUpper(stmt)
	if strings.Contains(upper, " USING GIN") || strings.Contains(upper, "GIN_TRGM_OPS") {
		return "", false
	}
	stmt = sqliteIndexConcurrentlyRE.ReplaceAllString(stmt, "")
	stmt = regexp.MustCompile(`(?i)\bUSING\s+BTREE\b`).ReplaceAllString(stmt, "")
	// MySQL prefix indexes such as message(255) are represented as regular
	// column indexes in SQLite. Function calls are unaffected because the
	// argument here must be a numeric literal.
	stmt = regexp.MustCompile("(`[^`]+`|[A-Za-z_][A-Za-z0-9_$]*)\\s*\\(\\s*\\d+\\s*\\)").ReplaceAllString(stmt, "$1")
	return strings.TrimSpace(stmt), true
}

func sqliteIgnorableHistoricalIndexError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such column") || strings.Contains(message, "no such table")
}

func sqliteColumnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_xinfo(?)", tableName)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func sqliteIntegrityCheck(ctx context.Context, db *sql.DB, pragma string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA "+pragma)
	if err != nil {
		return fmt.Errorf("sqlite %s: %w", pragma, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("sqlite %s result: %w", pragma, err)
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			return fmt.Errorf("sqlite %s failed: %s", pragma, result)
		}
	}
	return rows.Err()
}

func splitSQLTopLevelComma(input string) []string {
	var parts []string
	var buf strings.Builder
	depth := 0
	inSingle, inDouble, inBacktick := false, false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch c {
		case '\'':
			if !inDouble && !inBacktick {
				if inSingle && i+1 < len(input) && input[i+1] == '\'' {
					_ = buf.WriteByte(c)
					_ = buf.WriteByte(input[i+1])
					i++
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && !inBacktick {
				inDouble = !inDouble
			}
		case '`':
			if !inSingle && !inDouble {
				inBacktick = !inBacktick
			}
		case '(':
			if !inSingle && !inDouble && !inBacktick {
				depth++
			}
		case ')':
			if !inSingle && !inDouble && !inBacktick && depth > 0 {
				depth--
			}
		case ',':
			if !inSingle && !inDouble && !inBacktick && depth == 0 {
				parts = append(parts, strings.TrimSpace(buf.String()))
				buf.Reset()
				continue
			}
		}
		_ = buf.WriteByte(c)
	}
	if rest := strings.TrimSpace(buf.String()); rest != "" {
		parts = append(parts, rest)
	}
	return parts
}

func firstSQLIdentifier(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if input[0] == '`' || input[0] == '"' {
		quote := input[0]
		if end := strings.IndexByte(input[1:], quote); end >= 0 {
			return input[1 : end+1]
		}
	}
	end := 0
	for end < len(input) {
		c := input[end]
		if c != '_' && c != '$' &&
			(c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') {
			break
		}
		end++
	}
	return input[:end]
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func compactSQL(stmt string) string {
	return strings.Join(strings.Fields(stmt), " ")
}
