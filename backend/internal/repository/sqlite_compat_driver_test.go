package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"ikik-api/internal/config"
)

func openSQLiteCompatTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open(sqliteCompatDriverName, dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestRewriteSQLiteQuery(t *testing.T) {
	query := `
INSERT IGNORE INTO counters (id, value, updated_at) VALUES (?, ?, NOW())
ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at);
SELECT CAST(value AS SIGNED), CAST(LEFT(created_at, 23) AS DATETIME), @@session.time_zone FROM counters WHERE id = ? FOR UPDATE;
SELECT PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) OVER () FROM counters;
SELECT NOW() - INTERVAL ? SECOND, expires_at + INTERVAL 7 DAY;
SELECT 'FOR UPDATE and INTERVAL 1 DAY';`

	rewritten := rewriteSQLiteQuery(query)
	require.Contains(t, rewritten, "INSERT OR IGNORE INTO counters")
	require.Contains(t, rewritten, "ON CONFLICT DO UPDATE SET")
	require.Contains(t, rewritten, "value = excluded.value")
	require.Contains(t, rewritten, "updated_at = excluded.updated_at")
	require.Contains(t, rewritten, "CAST(value AS INTEGER)")
	require.Contains(t, rewritten, "datetime(LEFT(created_at, 23))")
	require.Contains(t, rewritten, "'UTC'")
	require.NotContains(t, rewritten, "WHERE id = ? FOR UPDATE")
	require.Contains(t, rewritten, "percentile_cont(duration_ms, 0.95) OVER ()")
	require.Contains(t, rewritten, "mysql_datetime_add(NOW(), ?, 'second', -1)")
	require.Contains(t, rewritten, "mysql_datetime_add(expires_at, 7, 'day', 1)")
	require.Contains(t, rewritten, "'FOR UPDATE and INTERVAL 1 DAY'")
}

func TestSQLiteCompatTruncateTable(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	_, err := db.Exec(`CREATE TABLE truncate_test (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO truncate_test (id) VALUES (1), (2)`)
	require.NoError(t, err)

	_, err = db.Exec(`TRUNCATE TABLE truncate_test`)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM truncate_test`).Scan(&count))
	require.Zero(t, count)
	require.Equal(t, `SELECT 'TRUNCATE TABLE truncate_test'`, rewriteSQLiteQuery(`SELECT 'TRUNCATE TABLE truncate_test'`))
	require.Equal(t, `SELECT 'escaped\' TRUNCATE TABLE truncate_test'`, rewriteSQLiteQuery(`SELECT 'escaped\' TRUNCATE TABLE truncate_test'`))
}
func TestRewriteSQLiteNullSafeEqualityAndUpdateAlias(t *testing.T) {
	query := `SELECT account_id <=> ?, group_id<=>?, '<=>' AS literal;
UPDATE scheduler_outbox o SET available_at = NOW() WHERE o.account_id <=> ?;
UPDATE ` + "`scheduler_outbox` `o`" + ` SET available_at = NOW() WHERE ` + "`o`.`group_id`" + ` <=> ?;`

	rewritten := rewriteSQLiteQuery(query)
	normalized := strings.Join(strings.Fields(rewritten), " ")
	require.Contains(t, normalized, "account_id IS ?")
	require.Contains(t, normalized, "group_id IS ?")
	require.Contains(t, normalized, "'<=>' AS literal")
	require.Contains(t, normalized, "UPDATE scheduler_outbox AS o SET")
	require.Contains(t, normalized, "UPDATE `scheduler_outbox` AS `o` SET")
	require.NotContains(t, normalized, "UPDATE scheduler_outbox o SET")
}

func TestSQLiteCompatRewritesDateColumnComparisons(t *testing.T) {
	query := `SELECT * FROM usage_dashboard_daily WHERE bucket_date = ? AND s.snapshot_date >= ? AND note = 'bucket_date = ?'`
	rewritten := rewriteSQLiteQuery(query)
	require.Contains(t, rewritten, "bucket_date = DATE(?)")
	require.Contains(t, rewritten, "s.snapshot_date >= DATE(?)")
	require.Contains(t, rewritten, "'bucket_date = ?'")
}

func TestSQLiteCompatDriverRewritesStatements(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	_, err := db.Exec(`CREATE TABLE counters (id INTEGER PRIMARY KEY, value TEXT, updated_at TEXT)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT IGNORE INTO counters (id, value, updated_at) VALUES (?, ?, NOW())`, 1, "first")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT IGNORE INTO counters (id, value, updated_at) VALUES (?, ?, NOW())`, 1, "ignored")
	require.NoError(t, err)

	stmt, err := db.Prepare(`INSERT INTO counters (id, value, updated_at) VALUES (?, ?, NOW()) ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at)`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stmt.Close()) })
	_, err = stmt.Exec(1, "updated")
	require.NoError(t, err)

	var value string
	err = db.QueryRow(`SELECT value FROM counters WHERE id = ? FOR UPDATE`, 1).Scan(&value)
	require.NoError(t, err)
	require.Equal(t, "updated", value)

	_, err = db.Exec(`UPDATE counters c SET value = ? WHERE c.id = ?`, "aliased", 1)
	require.NoError(t, err)
	err = db.QueryRow(`SELECT value FROM counters WHERE id = ?`, 1).Scan(&value)
	require.NoError(t, err)
	require.Equal(t, "aliased", value)

	var bothNull, bothOne, nullAndOne int
	err = db.QueryRow(`SELECT NULL <=> NULL, 1 <=> 1, NULL <=> 1`).Scan(&bothNull, &bothOne, &nullAndOne)
	require.NoError(t, err)
	require.Equal(t, 1, bothNull)
	require.Equal(t, 1, bothOne)
	require.Equal(t, 0, nullAndOne)

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT IGNORE INTO counters (id, value) VALUES (?, ?)`, 2, "tx")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestSQLiteCompatInsertSelectUpsert(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	_, err := db.Exec(`CREATE TABLE aggregate_values (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`)
	require.NoError(t, err)

	query := `
		INSERT INTO aggregate_values (id, value)
		SELECT source.id, source.value
		FROM (SELECT 1 AS id, 10 AS value) source
		WHERE TRUE
		ON DUPLICATE KEY UPDATE value = VALUES(value)`
	_, err = db.Exec(query)
	require.NoError(t, err)
	_, err = db.Exec(strings.Replace(query, "10 AS value", "20 AS value", 1))
	require.NoError(t, err)

	var value int
	require.NoError(t, db.QueryRow(`SELECT value FROM aggregate_values WHERE id = 1`).Scan(&value))
	require.Equal(t, 20, value)
}

func TestSQLiteCompatNormalizesBoundTimes(t *testing.T) {
	db := openSQLiteCompatTestDB(t)
	_, err := db.Exec(`CREATE TABLE events (occurred_at DATETIME PRIMARY KEY)`)
	require.NoError(t, err)

	occurredAt := time.Date(2026, time.August, 23, 12, 34, 56, 123456789, time.FixedZone("UTC+8", 8*60*60))
	_, err = db.Exec(`INSERT INTO events (occurred_at) VALUES (?)`, occurredAt)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM events WHERE occurred_at = ?`, occurredAt).Scan(&count))
	require.Equal(t, 1, count)

	var stored string
	require.NoError(t, db.QueryRow(`SELECT CAST(occurred_at AS TEXT) FROM events`).Scan(&stored))
	require.Equal(t, "2026-08-23 04:34:56.123456", stored)
}

func TestSQLiteCompatFunctions(t *testing.T) {
	db := openSQLiteCompatTestDB(t)

	var (
		position, signedValue, greatest, least int64
		unquoted, merged, formatted, converted string
		prefix, suffix, conditional            string
		unixTime                               int64
		fromUnix, timezone                     string
	)
	err := db.QueryRow(`
SELECT
  FIND_IN_SET('b', 'a,b,c'),
  JSON_UNQUOTE('"hello"'),
  JSON_MERGE_PATCH('{"a":1,"nested":{"x":1}}', '{"a":null,"nested":{"y":2}}'),
  DATE_FORMAT('2024-01-02 03:04:05.123456', '%Y-%m-%d %H:%i:%s.%f'),
  CONVERT_TZ('2024-01-02 03:04:05', 'UTC', '+08:00'),
  SUBSTRING_INDEX('a.b.c', '.', 2),
  SUBSTRING_INDEX('a.b.c', '.', -2),
  GREATEST(1, 9, 3),
  LEAST(1, 9, 3),
  IF(1, 'yes', 'no'),
  UNIX_TIMESTAMP('1970-01-01 00:00:01'),
  FROM_UNIXTIME(0),
  @@session.time_zone,
  CAST('42' AS SIGNED)
`).Scan(&position, &unquoted, &merged, &formatted, &converted, &prefix, &suffix, &greatest, &least, &conditional, &unixTime, &fromUnix, &timezone, &signedValue)
	require.NoError(t, err)
	require.Equal(t, int64(2), position)
	require.Equal(t, "hello", unquoted)
	require.JSONEq(t, `{"nested":{"x":1,"y":2}}`, merged)
	require.Equal(t, "2024-01-02 03:04:05.123456", formatted)
	require.Equal(t, "2024-01-02 11:04:05", converted)
	require.Equal(t, "a.b", prefix)
	require.Equal(t, "b.c", suffix)
	require.Equal(t, int64(9), greatest)
	require.Equal(t, int64(1), least)
	require.Equal(t, "yes", conditional)
	require.Equal(t, int64(1), unixTime)
	require.Equal(t, "1970-01-01 00:00:00.000000", fromUnix)
	require.Equal(t, "UTC", timezone)
	require.Equal(t, int64(42), signedValue)

	var now, utcNow, currentDate, preciseNow string
	err = db.QueryRow(`SELECT NOW(), UTC_TIMESTAMP(), CURDATE(), NOW(6)`).Scan(&now, &utcNow, &currentDate, &preciseNow)
	require.NoError(t, err)
	_, err = time.Parse("2006-01-02 15:04:05", now)
	require.NoError(t, err)
	_, err = time.Parse("2006-01-02 15:04:05", utcNow)
	require.NoError(t, err)
	_, err = time.Parse("2006-01-02", currentDate)
	require.NoError(t, err)
	_, err = time.Parse("2006-01-02 15:04:05.000000", preciseNow)
	require.NoError(t, err)
}

func TestSQLiteCompatMySQLISOWeekFormatting(t *testing.T) {
	db := openSQLiteCompatTestDB(t)

	var yearBoundary, firstWeek string
	err := db.QueryRow(`SELECT DATE_FORMAT('2021-01-01 12:00:00', '%x-W%v'), DATE_FORMAT('2024-01-01 12:00:00', '%x-W%v')`).Scan(&yearBoundary, &firstWeek)
	require.NoError(t, err)
	require.Equal(t, "2020-W53", yearBoundary)
	require.Equal(t, "2024-W01", firstWeek)
}

func TestSQLiteCompatMySQLUtilityFunctionsAndDatetimeCast(t *testing.T) {
	db := openSQLiteCompatTestDB(t)

	var regexpMatch, regexpMiss int64
	err := db.QueryRow(`SELECT '12345' REGEXP '^[0-9]+$', 'abc' REGEXP '^[0-9]+$'`).Scan(&regexpMatch, &regexpMiss)
	require.NoError(t, err)
	require.Equal(t, int64(1), regexpMatch)
	require.Equal(t, int64(0), regexpMiss)

	var left, concatenated string
	var dayOfWeek, nullValue, nonNullValue int64
	var converted string
	err = db.QueryRow(`
SELECT
  LEFT('abcdef', 3),
  CONCAT('a', 2, 'c'),
  DAYOFWEEK('2024-01-07 12:00:00'),
  ISNULL(NULL),
  ISNULL('value'),
  CAST(LEFT('2024-01-02T03:04:05.123Z', 23) AS DATETIME)
`).Scan(&left, &concatenated, &dayOfWeek, &nullValue, &nonNullValue, &converted)
	require.NoError(t, err)
	require.Equal(t, "abc", left)
	require.Equal(t, "a2c", concatenated)
	require.Equal(t, int64(1), dayOfWeek)
	require.Equal(t, int64(1), nullValue)
	require.Equal(t, int64(0), nonNullValue)
	require.Equal(t, "2024-01-02 03:04:05", converted)
}

func TestSQLiteCompatPercentileAndIntervals(t *testing.T) {
	db := openSQLiteCompatTestDB(t)

	var percentile float64
	err := db.QueryRow(`
WITH samples(value) AS (VALUES (10), (20), (30), (40))
SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY value) FROM samples
`).Scan(&percentile)
	require.NoError(t, err)
	require.InDelta(t, 25, percentile, 0.000001)

	var fixed, parameterized, nested, boundTime, monthEnd, microseconds string
	err = db.QueryRow(`SELECT '2024-01-02 03:04:05' + INTERVAL 2 HOUR`).Scan(&fixed)
	require.NoError(t, err)
	require.Equal(t, "2024-01-02 05:04:05", fixed)
	err = db.QueryRow(`SELECT ? + INTERVAL ? DAY`, "2024-01-02 03:04:05", 3).Scan(&parameterized)
	require.NoError(t, err)
	require.Equal(t, "2024-01-05 03:04:05", parameterized)
	err = db.QueryRow(`SELECT ('2024-01-02 03:04:05' + INTERVAL 2 HOUR) + INTERVAL 1 DAY`).Scan(&nested)
	require.NoError(t, err)
	require.Equal(t, "2024-01-03 05:04:05", nested)
	err = db.QueryRow(`SELECT ? + INTERVAL 1 DAY`, time.Date(2024, 1, 2, 3, 4, 5, 123000000, time.UTC)).Scan(&boundTime)
	require.NoError(t, err)
	require.Equal(t, "2024-01-03 03:04:05.123", boundTime)
	err = db.QueryRow(`SELECT '2024-01-31 03:04:05' + INTERVAL 1 MONTH`).Scan(&monthEnd)
	require.NoError(t, err)
	require.Equal(t, "2024-02-29 03:04:05", monthEnd)
	err = db.QueryRow(`SELECT '2024-01-02 03:04:05' + INTERVAL 1250 MICROSECOND`).Scan(&microseconds)
	require.NoError(t, err)
	require.Equal(t, "2024-01-02 03:04:05.001250", microseconds)
}

func TestOpenSQLDatabaseUsesSQLiteCompatDriverAndDialect(t *testing.T) {
	path := t.TempDir() + "/compat.db"
	db, dialectName, err := OpenSQLDatabase(&config.DatabaseConfig{Driver: config.DatabaseDriverSQLite, Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.Equal(t, "sqlite3", dialectName)

	var got int
	err = db.QueryRow(`SELECT IF(FIND_IN_SET('2', '1,2,3'), 7, 0)`).Scan(&got)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}
