package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"ikik-api/internal/service"
	_ "modernc.org/sqlite"
)

func openJSONTableSQLiteTestDB(t *testing.T) *sql.DB {
	t.Helper()

	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Ping())
	return db
}

func TestListRecentHistoryForMonitorsSQLiteJSONPairs(t *testing.T) {
	db := openJSONTableSQLiteTestDB(t)
	_, err := db.Exec(`
		CREATE TABLE channel_monitor_histories (
			id INTEGER PRIMARY KEY,
			monitor_id INTEGER NOT NULL,
			model TEXT NOT NULL,
			status TEXT NOT NULL,
			latency_ms INTEGER,
			ping_latency_ms INTEGER,
			checked_at DATETIME NOT NULL
		)
	`)
	require.NoError(t, err)

	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		monitorID int64
		model     string
		status    string
		latency   int
		checkedAt time.Time
	}{
		{1, "model-a", "operational", 10, base},
		{1, "model-a", "degraded", 20, base.Add(time.Minute)},
		{1, "other-model", "failed", 30, base.Add(2 * time.Minute)},
		{2, "模型-b", "operational", 40, base.Add(3 * time.Minute)},
	}
	for _, row := range rows {
		_, err = db.Exec(`
			INSERT INTO channel_monitor_histories
				(monitor_id, model, status, latency_ms, ping_latency_ms, checked_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, row.monitorID, row.model, row.status, row.latency, row.latency+1, row.checkedAt)
		require.NoError(t, err)
	}

	repo := &channelMonitorRepository{db: db}
	got, err := repo.ListRecentHistoryForMonitors(
		context.Background(),
		[]int64{1, 2},
		map[int64]string{1: "model-a", 2: "模型-b"},
		1,
	)
	require.NoError(t, err)
	require.Len(t, got[1], 1)
	require.Equal(t, "degraded", got[1][0].Status)
	require.Equal(t, 20, *got[1][0].LatencyMs)
	require.Len(t, got[2], 1)
	require.Equal(t, "operational", got[2][0].Status)
	require.Equal(t, 40, *got[2][0].LatencyMs)
}

func TestSetGroupIDsTxSQLiteJSONArray(t *testing.T) {
	db := openJSONTableSQLiteTestDB(t)
	_, err := db.Exec(`
		CREATE TABLE channel_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER NOT NULL,
			group_id INTEGER NOT NULL UNIQUE,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, setGroupIDsTx(ctx, db, 7, []int64{101, 202}))
	require.NoError(t, setGroupIDsTx(ctx, db, 7, []int64{303}))

	var channelID, groupID int64
	err = db.QueryRow(`SELECT channel_id, group_id FROM channel_groups`).Scan(&channelID, &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(7), channelID)
	require.Equal(t, int64(303), groupID)
}

type groupBindSQLiteExecutor struct {
	*sql.DB
}

func (e groupBindSQLiteExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "scheduler_outbox") {
		return e.DB.ExecContext(ctx, "SELECT 1")
	}
	return e.DB.ExecContext(ctx, query, args...)
}

func TestBindAccountsToGroupSQLiteJSONArray(t *testing.T) {
	db := openJSONTableSQLiteTestDB(t)
	_, err := db.Exec(`
		CREATE TABLE account_groups (
			account_id INTEGER NOT NULL,
			group_id INTEGER NOT NULL,
			priority INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (account_id, group_id)
		)
	`)
	require.NoError(t, err)

	repo := newGroupRepositoryWithSQL(nil, groupBindSQLiteExecutor{DB: db})
	ctx := context.Background()
	require.NoError(t, repo.BindAccountsToGroup(ctx, 9, []int64{11, 22}))
	require.NoError(t, repo.BindAccountsToGroup(ctx, 9, []int64{11, 22}))

	rows, err := db.Query(`SELECT account_id, group_id, priority FROM account_groups ORDER BY account_id`)
	require.NoError(t, err)
	defer rows.Close()

	var got [][3]int64
	for rows.Next() {
		var row [3]int64
		require.NoError(t, rows.Scan(&row[0], &row[1], &row[2]))
		got = append(got, row)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, [][3]int64{{11, 9, 50}, {22, 9, 50}}, got)
}

func TestUserGroupRateRepositorySQLiteJSONPairsAndUpserts(t *testing.T) {
	db := openJSONTableSQLiteTestDB(t)
	_, err := db.Exec(`
		CREATE TABLE user_group_rate_multipliers (
			user_id INTEGER NOT NULL,
			group_id INTEGER NOT NULL,
			rate_multiplier REAL,
			rpm_override INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, group_id)
		)
	`)
	require.NoError(t, err)

	repo := NewUserGroupRateRepository(db)
	ctx := context.Background()

	rate := 1.25
	require.NoError(t, repo.SyncUserGroupRates(ctx, 10, map[int64]*float64{100: &rate}))
	rate = 2.5
	require.NoError(t, repo.SyncUserGroupRates(ctx, 10, map[int64]*float64{100: &rate}))
	var gotRate float64
	require.NoError(t, db.QueryRow(`
		SELECT rate_multiplier FROM user_group_rate_multipliers
		WHERE user_id = 10 AND group_id = 100
	`).Scan(&gotRate))
	require.InDelta(t, 2.5, gotRate, 0.000001)

	require.NoError(t, repo.SyncGroupRateMultipliers(ctx, 200, []service.GroupRateMultiplierInput{
		{UserID: 20, RateMultiplier: 1.1},
		{UserID: 21, RateMultiplier: 1.2},
	}))
	require.NoError(t, repo.SyncGroupRateMultipliers(ctx, 200, []service.GroupRateMultiplierInput{
		{UserID: 20, RateMultiplier: 1.8},
	}))
	require.NoError(t, db.QueryRow(`
		SELECT rate_multiplier FROM user_group_rate_multipliers
		WHERE user_id = 20 AND group_id = 200
	`).Scan(&gotRate))
	require.InDelta(t, 1.8, gotRate, 0.000001)
	var removedCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM user_group_rate_multipliers
		WHERE user_id = 21 AND group_id = 200
	`).Scan(&removedCount))
	require.Zero(t, removedCount)

	rpm30, rpm60 := 30, 60
	require.NoError(t, repo.SyncGroupRPMOverrides(ctx, 300, []service.GroupRPMOverrideInput{
		{UserID: 30, RPMOverride: &rpm30},
	}))
	require.NoError(t, repo.SyncGroupRPMOverrides(ctx, 300, []service.GroupRPMOverrideInput{
		{UserID: 30, RPMOverride: &rpm60},
	}))
	var gotRPM int
	require.NoError(t, db.QueryRow(`
		SELECT rpm_override FROM user_group_rate_multipliers
		WHERE user_id = 30 AND group_id = 300
	`).Scan(&gotRPM))
	require.Equal(t, 60, gotRPM)
}
