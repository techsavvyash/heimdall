package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

const retirementIndexChecksum = "c9294b44dcda4b972e662d8a874ed7c9ce821e2f1379ce0b2249570d7b70d7ab"

// Compare the v5 catalog after removing only the exact supported index signature.
var retirementIndexCatalogSQL = `WITH catalog AS (` + auditRetentionSchemaSQL + `),
 index_signature AS (
 SELECT '["heimdall_retention","retirement_member_lookup","i","p",false,false,false,"gin"]'::jsonb AS relation,
 '["heimdall_retention","retirement_member_lookup","CREATE INDEX retirement_member_lookup ON heimdall_retention.retirements USING gin (member_event_ids)",true,true,true,true,false,false,false,false]'::jsonb AS definition
 ), checked AS (
 SELECT value, relation, definition, $1::boolean
 AND (value->'relations') @> jsonb_build_array(relation)
 AND (value->'indexes') @> jsonb_build_array(definition)
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE oid=to_regclass('heimdall_retention.retirement_member_lookup') AND reloptions IS NULL)
 AS indexed FROM catalog CROSS JOIN index_signature
 )
 SELECT (CASE WHEN indexed THEN jsonb_set(jsonb_set(value,'{relations}',
 (SELECT jsonb_agg(item ORDER BY position) FROM jsonb_array_elements(value->'relations') WITH ORDINALITY AS r(item,position) WHERE item<>relation)),
 '{indexes}',(SELECT jsonb_agg(item ORDER BY position) FROM jsonb_array_elements(value->'indexes') WITH ORDINALITY AS i(item,position) WHERE item<>definition))
 ELSE value END)::text, current_setting('server_version_num')::integer BETWEEN 170000 AND 179999
 AND current_setting('session_replication_role')='origin', indexed FROM checked`

// InstallRetirementIndex is a physical v5 migration shared by both SDKs.
func InstallRetirementIndex(ctx context.Context, db *sql.DB, source []byte, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained {
		return contract.ErrInvalidArgument
	}
	digest := sha256.Sum256(source)
	if hex.EncodeToString(digest[:]) != retirementIndexChecksum {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := retainedConnection(ctx, db, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='20s'; SELECT pg_advisory_xact_lock(74162401)"); err != nil {
		return databaseError(ctx)
	}
	var ready bool
	if err = tx.QueryRowContext(ctx, retentionV5ReadinessSQL, retentionReadinessArgs()...).Scan(&ready); err != nil || !ready {
		return databaseError(ctx)
	}
	indexed, err := checkHoldReleaseCatalog(ctx, tx, true)
	if err != nil {
		return err
	}
	if !indexed {
		if _, err = tx.ExecContext(ctx, string(source)); err != nil {
			return databaseError(ctx)
		}
		if indexed, err = checkHoldReleaseCatalog(ctx, tx, true); err != nil {
			return err
		}
		if !indexed {
			return contract.ErrAuditUnavailable
		}
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}
