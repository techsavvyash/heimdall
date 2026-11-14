package postgres

import (
	"database/sql/driver"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// NewConnector creates a cancellation-aware PostgreSQL connector without connecting.
// Use sql.OpenDB(connector); the application owns pool configuration and Close.
// Connection establishment is limited to five seconds or the earlier context.
// TLS and credentials follow the supplied DSN; this does not weaken TLS settings.
func NewConnector(dsn string) (driver.Connector, error) { return internal.NewConnector(dsn) }
