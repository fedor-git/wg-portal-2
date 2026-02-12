package config

import "time"

// SupportedDatabase is a type for the supported database types.
// Supported: mysql, mssql, postgres, sqlite
type SupportedDatabase string

const (
	DatabaseMySQL    SupportedDatabase = "mysql"
	DatabaseMsSQL    SupportedDatabase = "mssql"
	DatabasePostgres SupportedDatabase = "postgres"
	DatabaseSQLite   SupportedDatabase = "sqlite"
)

// DatabaseConfig contains the configuration for the database connection.
type DatabaseConfig struct {
	// Debug enables logging of all database statements
	Debug bool `yaml:"debug"`
	// SlowQueryThreshold enables logging of slow queries which take longer than the specified duration
	SlowQueryThreshold time.Duration `yaml:"slow_query_threshold"` // "0" means no logging of slow queries
	// Type is the database type. Supported: mysql, mssql, postgres, sqlite
	Type SupportedDatabase `yaml:"type"`
	// DSN is the database connection string.
	// For SQLite, it is the path to the database file.
	// For other databases, it is the connection string, see: https://gorm.io/docs/connecting_to_the_database.html
	DSN string `yaml:"dsn"`
	// EncryptionPassphrase is the passphrase used to encrypt sensitive data (WireGuard keys) in the database.
	// If no passphrase is provided, no encryption will be used.
	EncryptionPassphrase string `yaml:"encryption_passphrase"`
	// MaxOpenConnections is the maximum number of open connections to the database.
	MaxOpenConnections int `yaml:"max_open_connections"`
	// MaxIdleConnections is the maximum number of idle connections in the connection pool.
	MaxIdleConnections int `yaml:"max_idle_connections"`
	// ConnectionMaxLifetime is the maximum lifetime of a connection (e.g., "3m", "1h").
	ConnectionMaxLifetime time.Duration `yaml:"connection_max_lifetime"`
}
