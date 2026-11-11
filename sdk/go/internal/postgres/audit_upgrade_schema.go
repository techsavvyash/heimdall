package postgres

import (
	"context"
	"database/sql"
	_ "embed"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// Catalog output is pinned to the tested PostgreSQL 17 profile. Ownership, grants,
// sequence position and data are deliberately not structural-schema assertions.
//
//go:embed audit_upgrade_schema.sql
var auditUpgradeSchemaSQL string

//go:embed audit_upgrade_schema.json
var auditUpgradeSchemaJSON string

func checkAuditUpgradeSchema(ctx context.Context, tx *sql.Tx) error {
	// A fixed search path makes deparsed definitions independent of connection setup.
	if _, e := tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; LOCK TABLE heimdall.schema_metadata,heimdall.migrations,heimdall.records,heimdall.identity_denials,heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall.audit_decision_backfill,heimdall.audit_upgrade IN SHARE MODE"); e != nil {
		return databaseError(ctx)
	}
	var matches bool
	if e := tx.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::integer BETWEEN 170000 AND 179999 AND current_setting('session_replication_role')='origin' AND actual.value=$1::jsonb FROM ("+auditUpgradeSchemaSQL+") actual", auditUpgradeSchemaJSON).Scan(&matches); e != nil {
		return databaseError(ctx)
	}
	if !matches {
		return contract.ErrAuditUnavailable
	}
	return nil
}
