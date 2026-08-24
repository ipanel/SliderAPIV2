package setup

import (
	"bufio"
	"strings"
	"testing"
)

func TestConfigureDatabaseDefaultsToSQLite(t *testing.T) {
	cfg := &DatabaseConfig{}
	passwordPrompted := false

	configureDatabase(bufio.NewReader(strings.NewReader("\ncustom.db\n")), cfg, func(string) string {
		passwordPrompted = true
		return "unexpected"
	})

	if cfg.Driver != defaultDatabaseDriver {
		t.Fatalf("driver=%q, want %q", cfg.Driver, defaultDatabaseDriver)
	}
	if cfg.Path != "custom.db" {
		t.Fatalf("path=%q, want custom.db", cfg.Path)
	}
	if cfg.Host != "" || cfg.Port != 0 || cfg.User != "" || cfg.Password != "" || cfg.DBName != "" || cfg.SSLMode != "" {
		t.Fatalf("sqlite setup unexpectedly populated MySQL fields: %+v", *cfg)
	}
	if passwordPrompted {
		t.Fatal("sqlite setup prompted for a database password")
	}
}

func TestConfigureDatabasePromptsForMySQL(t *testing.T) {
	cfg := &DatabaseConfig{}
	reader := bufio.NewReader(strings.NewReader("mysql\ndb.example.com\n4406\napp_user\napp_db\nverify-full\n"))

	configureDatabase(reader, cfg, func(prompt string) string {
		if prompt != "MySQL/MariaDB Password" {
			t.Fatalf("password prompt=%q", prompt)
		}
		return "secret"
	})

	if cfg.Driver != "mysql" {
		t.Fatalf("driver=%q, want mysql", cfg.Driver)
	}
	if cfg.Path != "" {
		t.Fatalf("path=%q, want empty for MySQL", cfg.Path)
	}
	if cfg.Host != "db.example.com" || cfg.Port != 4406 || cfg.User != "app_user" || cfg.Password != "secret" || cfg.DBName != "app_db" || cfg.SSLMode != "verify-full" {
		t.Fatalf("unexpected MySQL config: %+v", *cfg)
	}
}

func TestConfigureDatabaseRejectsUnknownDriver(t *testing.T) {
	cfg := &DatabaseConfig{}

	configureDatabase(bufio.NewReader(strings.NewReader("postgres\nsqlite\napp.db\n")), cfg, func(string) string {
		t.Fatal("sqlite setup prompted for a database password")
		return ""
	})

	if cfg.Driver != "sqlite" || cfg.Path != "app.db" {
		t.Fatalf("unexpected database config: %+v", *cfg)
	}
}

func TestDatabaseSummaryMatchesDriver(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConfig
		want string
	}{
		{
			name: "sqlite",
			cfg:  DatabaseConfig{Driver: "sqlite", Path: "data/app.db"},
			want: "SQLite (data/app.db)",
		},
		{
			name: "mysql",
			cfg: DatabaseConfig{
				Driver:  "mysql",
				Host:    "db.example.com",
				Port:    3306,
				User:    "app",
				DBName:  "ikik_api",
				SSLMode: "require",
			},
			want: "MySQL/MariaDB (app@db.example.com:3306/ikik_api, SSL: require)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseSummary(&tt.cfg); got != tt.want {
				t.Fatalf("databaseSummary()=%q, want %q", got, tt.want)
			}
		})
	}
}
