// Package repository 提供应用程序的基础设施层组件。
// 包括数据库连接初始化、ORM 客户端管理、Redis 连接、数据库迁移等核心功能。
package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ikik-api/ent"
	"ikik-api/internal/config"
	"ikik-api/internal/pkg/timezone"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-sql-driver/mysql"
)

// InitEnt 初始化 Ent ORM 客户端并返回客户端实例和底层的 *sql.DB。
//
// 该函数执行以下操作：
//  1. 初始化全局时区设置，确保时间处理一致性
//  2. 建立 MySQL/MariaDB 或 SQLite 数据库连接
//  3. 自动执行数据库迁移，确保 schema 与代码同步
//  4. 创建并返回 Ent 客户端实例
//
// 重要提示：调用者必须负责关闭返回的 ent.Client（关闭时会自动关闭底层的 driver/db）。
//
// 参数：
//   - cfg: 应用程序配置，包含数据库连接信息和时区设置
//
// 返回：
//   - *ent.Client: Ent ORM 客户端，用于执行数据库操作
//   - *sql.DB: 底层的 SQL 数据库连接，可用于直接执行原生 SQL
//   - error: 初始化过程中的错误
func InitEnt(cfg *config.Config) (*ent.Client, *sql.DB, error) {
	// 优先初始化时区设置，确保所有时间操作使用统一的时区。
	// 这对于跨时区部署和日志时间戳的一致性至关重要。
	if err := timezone.Init(cfg.Timezone); err != nil {
		return nil, nil, err
	}

	// 构建包含时区信息的数据库连接字符串 (DSN)。
	db, dialectName, err := OpenSQLDatabase(&cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	applyDBPoolSettings(db, cfg)

	drv := entsql.OpenDB(dialectName, db)
	client := ent.NewClient(ent.Driver(drv))

	// MySQL uses versioned SQL migrations; SQLite creates the current Ent schema.
	migrationCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := initializeDatabaseSchema(migrationCtx, client, db, dialectName); err != nil {
		_ = client.Close()
		return nil, nil, err
	}

	// Bootstrap secrets after the schema is ready.
	if err := ensureBootstrapSecrets(migrationCtx, client, cfg); err != nil {
		_ = client.Close()
		return nil, nil, err
	}

	// 在密钥补齐后执行完整配置校验，避免空 jwt.secret 导致服务运行时失败。
	if err := cfg.Validate(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("validate config after secret bootstrap: %w", err)
	}

	// SIMPLE 模式：启动时补齐各平台默认分组。
	// - anthropic/openai/gemini: 确保存在 <platform>-default
	// - antigravity: 仅要求存在 >=2 个未软删除分组（用于 claude/gemini 混合调度场景）
	if cfg.RunMode == config.RunModeSimple {
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer seedCancel()
		if err := ensureSimpleModeDefaultGroups(seedCtx, client); err != nil {
			_ = client.Close()
			return nil, nil, err
		}
		if err := ensureSimpleModeAdminConcurrency(seedCtx, client); err != nil {
			_ = client.Close()
			return nil, nil, err
		}
	}

	return client, drv.DB(), nil
}

// OpenSQLDatabase opens a database connection for the configured driver.
func OpenSQLDatabase(cfg *config.DatabaseConfig) (*sql.DB, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("database config is nil")
	}

	switch cfg.DriverName() {
	case config.DatabaseDriverSQLite:
		path := strings.TrimSpace(cfg.Path)
		if path == "" {
			return nil, "", fmt.Errorf("sqlite database path is required")
		}
		if !strings.HasPrefix(strings.ToLower(path), "file:") {
			dir := filepath.Dir(path)
			if dir != "." {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return nil, "", fmt.Errorf("create sqlite database directory: %w", err)
				}
			}
		}
		db, err := sql.Open(sqliteCompatDriverName, cfg.DSN())
		if err != nil {
			return nil, "", fmt.Errorf("open sqlite database: %w", err)
		}
		return db, dialect.SQLite, nil
	case config.DatabaseDriverMySQL:
		mysqlConfig, err := buildMySQLConfig(cfg)
		if err != nil {
			return nil, "", err
		}
		connector, err := mysql.NewConnector(mysqlConfig)
		if err != nil {
			return nil, "", fmt.Errorf("create mysql connector: %w", err)
		}
		return sql.OpenDB(connector), dialect.MySQL, nil
	default:
		return nil, "", fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}
}

func buildMySQLConfig(cfg *config.DatabaseConfig) (*mysql.Config, error) {
	mysqlConfig, err := mysql.ParseDSN(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse mysql DSN: %w", err)
	}

	tlsConfig, err := buildMySQLTLSConfig(cfg.SSLMode, mysqlConfig.Addr)
	if err != nil {
		return nil, err
	}
	mysqlConfig.TLSConfig = ""
	mysqlConfig.TLS = tlsConfig
	mysqlConfig.AllowFallbackToPlaintext = false
	return mysqlConfig, nil
}

func buildMySQLTLSConfig(sslMode, addr string) (*tls.Config, error) {
	serverName := mysqlServerName(addr)

	switch strings.ToLower(strings.TrimSpace(sslMode)) {
	case "", "disable":
		return nil, nil
	case "require":
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         serverName,
			InsecureSkipVerify: true, //nolint:gosec // require encrypts the connection but intentionally does not authenticate the server certificate.
		}, nil
	case "verify-full":
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		}, nil
	case "verify-ca":
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         serverName,
			InsecureSkipVerify: true, //nolint:gosec // Certificate-chain verification is performed below without hostname verification.
			VerifyConnection: func(state tls.ConnectionState) error {
				return verifyMySQLCertificateChain(state, nil)
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported mysql sslmode %q (supported: disable, require, verify-ca, verify-full)", sslMode)
	}
}

func mysqlServerName(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func verifyMySQLCertificateChain(state tls.ConnectionState, roots *x509.CertPool) error {
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("verify mysql TLS certificate chain: server provided no certificates")
	}

	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("verify mysql TLS certificate chain: %w", err)
	}
	return nil
}

// InitializeDatabaseSchema initializes the schema for setup-time database creation.
func InitializeDatabaseSchema(ctx context.Context, db *sql.DB, dialectName string) error {
	if db == nil {
		return fmt.Errorf("nil sql db")
	}
	if dialectName == dialect.SQLite {
		drv := entsql.OpenDB(dialect.SQLite, db)
		client := ent.NewClient(ent.Driver(drv))
		if err := client.Schema.Create(ctx); err != nil {
			return err
		}
		return ApplySQLiteMigrations(ctx, db)
	}
	return ApplyMigrations(ctx, db)
}

func initializeDatabaseSchema(ctx context.Context, client *ent.Client, db *sql.DB, dialectName string) error {
	if dialectName == dialect.SQLite {
		if err := client.Schema.Create(ctx); err != nil {
			return err
		}
		return ApplySQLiteMigrations(ctx, db)
	}
	return ApplyMigrations(ctx, db)
}
