package repository

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"ikik-api/internal/config"

	"github.com/stretchr/testify/require"
)

func TestBuildMySQLConfigTLSModes(t *testing.T) {
	tests := []struct {
		name                     string
		sslMode                  string
		wantTLS                  bool
		wantInsecureSkipVerify   bool
		wantVerifyConnectionFunc bool
	}{
		{name: "empty disables TLS", sslMode: ""},
		{name: "disable", sslMode: "disable"},
		{name: "require", sslMode: "require", wantTLS: true, wantInsecureSkipVerify: true},
		{name: "verify full", sslMode: "verify-full", wantTLS: true},
		{name: "verify CA", sslMode: "verify-ca", wantTLS: true, wantInsecureSkipVerify: true, wantVerifyConnectionFunc: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mysqlConfig, err := buildMySQLConfig(mysqlDatabaseConfig(tt.sslMode))
			require.NoError(t, err)
			require.Empty(t, mysqlConfig.TLSConfig)
			require.False(t, mysqlConfig.AllowFallbackToPlaintext)

			if !tt.wantTLS {
				require.Nil(t, mysqlConfig.TLS)
				return
			}
			require.NotNil(t, mysqlConfig.TLS)
			require.Equal(t, uint16(tls.VersionTLS12), mysqlConfig.TLS.MinVersion)
			require.Equal(t, "mysql.example.test", mysqlConfig.TLS.ServerName)
			require.Equal(t, tt.wantInsecureSkipVerify, mysqlConfig.TLS.InsecureSkipVerify)
			require.Equal(t, tt.wantVerifyConnectionFunc, mysqlConfig.TLS.VerifyConnection != nil)
		})
	}
}

func TestBuildMySQLConfigRejectsUnknownSSLMode(t *testing.T) {
	_, err := buildMySQLConfig(mysqlDatabaseConfig("preferred"))
	require.ErrorContains(t, err, `unsupported mysql sslmode "preferred"`)

	db, dialectName, err := OpenSQLDatabase(mysqlDatabaseConfig("preferred"))
	require.ErrorContains(t, err, `unsupported mysql sslmode "preferred"`)
	require.Nil(t, db)
	require.Empty(t, dialectName)
}

func TestBuildMySQLConfigPreservesDSNOptions(t *testing.T) {
	mysqlConfig, err := buildMySQLConfig(mysqlDatabaseConfig("verify-full"))
	require.NoError(t, err)

	require.Equal(t, "app_user", mysqlConfig.User)
	require.Equal(t, "secret", mysqlConfig.Passwd)
	require.Equal(t, "tcp", mysqlConfig.Net)
	require.Equal(t, "mysql.example.test:3307", mysqlConfig.Addr)
	require.Equal(t, "app_db", mysqlConfig.DBName)
	require.True(t, mysqlConfig.ParseTime)
	require.True(t, mysqlConfig.MultiStatements)
	require.Equal(t, time.UTC, mysqlConfig.Loc)
	require.Equal(t, "'+00:00'", mysqlConfig.Params["time_zone"])

	formattedDSN := mysqlConfig.FormatDSN()
	require.Contains(t, formattedDSN, "charset=utf8mb4")
	require.Contains(t, formattedDSN, "parseTime=true")
	require.Contains(t, formattedDSN, "multiStatements=true")
	require.Contains(t, formattedDSN, "time_zone=%27%2B00%3A00%27")

	db, dialectName, err := OpenSQLDatabase(mysqlDatabaseConfig("verify-full"))
	require.NoError(t, err)
	require.Equal(t, "mysql", dialectName)
	require.NoError(t, db.Close())
}

func TestVerifyMySQLCertificateChainIgnoresHostname(t *testing.T) {
	root, leaf := createMySQLTestCertificateChain(t, "different.example.test")
	roots := x509.NewCertPool()
	roots.AddCert(root)
	state := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		ServerName:       "mysql.example.test",
	}

	_, hostnameErr := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   state.ServerName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.Error(t, hostnameErr)
	require.NoError(t, verifyMySQLCertificateChain(state, roots))
	require.Error(t, verifyMySQLCertificateChain(state, x509.NewCertPool()))
	require.ErrorContains(t, verifyMySQLCertificateChain(tls.ConnectionState{}, roots), "server provided no certificates")
}

func mysqlDatabaseConfig(sslMode string) *config.DatabaseConfig {
	return &config.DatabaseConfig{
		Driver:   config.DatabaseDriverMySQL,
		Host:     "mysql.example.test",
		Port:     3307,
		User:     "app_user",
		Password: "secret",
		DBName:   "app_db",
		SSLMode:  sslMode,
	}
}

func createMySQLTestCertificateChain(t *testing.T, dnsName string) (*x509.Certificate, *x509.Certificate) {
	t.Helper()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "MySQL Test Root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	root, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)

	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	return root, leaf
}

func TestBuildMySQLConfigNormalizesSSLMode(t *testing.T) {
	mysqlConfig, err := buildMySQLConfig(mysqlDatabaseConfig("  ReQuIrE  "))
	require.NoError(t, err)
	require.NotNil(t, mysqlConfig.TLS)
	require.True(t, mysqlConfig.TLS.InsecureSkipVerify)
}
