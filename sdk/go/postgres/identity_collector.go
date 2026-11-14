package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type IdentityAggregate struct{ Type, ID string }

type IdentityCollectorOptions struct {
	provisioning.IdentityEventReaderOptions
	ProductID, Environment, TenantID string
	AuditSinks                       []string
	StartMicros                      int64
	Aggregates                       []IdentityAggregate
}

// IdentityEventCoverage is supplied by the trusted source operator for each scan.
type IdentityEventCoverage struct{ RetainedFromMicros, VisibleBeforeMicros, ValidUntilMs int64 }
type IdentityCollection struct {
	NextMicros int64
	Recorded   int
}

type IdentityCollectorCheckpoint struct {
	NextMicros      int64
	MappingDigest   string
	MappingRevision int64
}

type IdentityCollector struct {
	db      *sql.DB
	options IdentityCollectorOptions
	reader  *provisioning.IdentityEventReader
	audit   *internal.RetainedRuntimeAudit
	allowed map[IdentityAggregate]bool
	mapping string
}

const maximumEventTime int64 = 253402300799999999
const identityCheckpointScope = "product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4 AND tenant_id=$5"

const identityCheckpointRead = "SELECT next_micros,mapping_digest,mapping_revision FROM heimdall_directory.event_checkpoints WHERE " + identityCheckpointScope

var collectorDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var collectorID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var collectorType = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// NewIdentityCollector collects certified historical windows for an explicitly mapped product tenant.
func NewIdentityCollector(db *sql.DB, o IdentityCollectorOptions) (*IdentityCollector, error) {
	if db == nil || !collectorID.MatchString(o.TenantID) || o.StartMicros < 1 ||
		o.StartMicros >= maximumEventTime || len(o.AuditSinks) == 0 || len(o.Aggregates) == 0 || len(o.Aggregates) > 10000 {
		return nil, contract.ErrInvalidArgument
	}
	allowed := make(map[IdentityAggregate]bool, len(o.Aggregates))
	for _, a := range o.Aggregates {
		if !collectorType.MatchString(a.Type) || !collectorID.MatchString(a.ID) {
			return nil, contract.ErrInvalidArgument
		}
		allowed[a] = true
	}
	o.Issuer = strings.TrimSuffix(o.Issuer, "/")
	reader, err := provisioning.NewIdentityEventReader(o.IdentityEventReaderOptions)
	if err != nil {
		return nil, err
	}
	audit, err := internal.NewRetainedRuntimeAudit(db, o.ProductID, o.Environment, o.AuditSinks, auditEventValidator(nil, false))
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(allowed))
	for a := range allowed {
		keys = append(keys, a.Type+":"+a.ID)
	}
	slices.Sort(keys)
	encoded, _ := json.Marshal(keys)
	mapping := sha256.Sum256(encoded)
	return &IdentityCollector{mapping: hex.EncodeToString(mapping[:]), db: db, options: o, reader: reader, audit: audit, allowed: allowed}, nil
}

func (c *IdentityCollector) checkpointScope() []any {
	o := c.options
	return []any{o.ProductID, o.Environment, o.Issuer, o.OrganizationID, o.TenantID}
}

// Checkpoint reads the active mapping revision and next timestamp.
func (c *IdentityCollector) Checkpoint(ctx context.Context) (*IdentityCollectorCheckpoint, error) {
	if c == nil || c.reader == nil || ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var current IdentityCollectorCheckpoint
	err := c.db.QueryRowContext(ctx, identityCheckpointRead, c.checkpointScope()...).Scan(&current.NextMicros, &current.MappingDigest, &current.MappingRevision)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, identityCollectionDBError(ctx)
	}
	return &current, nil
}

// ActivateMapping applies this collector's mapping at the existing checkpoint.
// Call from trusted operator code after approving the mapping and its boundary.
func (c *IdentityCollector) ActivateMapping(ctx context.Context, expected IdentityCollectorCheckpoint) (IdentityCollectorCheckpoint, error) {
	empty := IdentityCollectorCheckpoint{}
	if c == nil || c.reader == nil || ctx == nil || expected.NextMicros < 1 || expected.NextMicros > maximumEventTime ||
		expected.MappingRevision < 0 || expected.MappingRevision >= 9007199254740991 || !collectorDigest.MatchString(expected.MappingDigest) {
		return empty, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return empty, identityCollectionDBError(ctx)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return empty, identityCollectionDBError(ctx)
	}
	scope := c.checkpointScope()
	var current IdentityCollectorCheckpoint
	err = tx.QueryRowContext(ctx, identityCheckpointRead+" FOR UPDATE", scope...).Scan(&current.NextMicros, &current.MappingDigest, &current.MappingRevision)
	if err == sql.ErrNoRows {
		return empty, contract.ErrNotFound
	}
	if err != nil {
		return empty, identityCollectionDBError(ctx)
	}
	target := expected
	if target.MappingDigest != c.mapping {
		target.MappingDigest = c.mapping
		target.MappingRevision++
	}
	if current == target {
		return current, nil
	}
	if current != expected {
		return empty, contract.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE heimdall_directory.event_checkpoints SET mapping_digest=$6,mapping_revision=$7 WHERE "+identityCheckpointScope, append(scope, target.MappingDigest, target.MappingRevision)...); err != nil {
		return empty, identityCollectionDBError(ctx)
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, contract.ErrOutcomeUnknown
	}
	return target, nil
}

func (c *IdentityCollector) Collect(ctx context.Context, coverage IdentityEventCoverage) (IdentityCollection, error) {
	empty := IdentityCollection{}
	if c == nil || c.reader == nil || ctx == nil || coverage.RetainedFromMicros < 0 ||
		coverage.VisibleBeforeMicros <= coverage.RetainedFromMicros || coverage.VisibleBeforeMicros > maximumEventTime || coverage.VisibleBeforeMicros > time.Now().UnixMilli()*1000 {
		return empty, contract.ErrInvalidArgument
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	if coverage.ValidUntilMs <= time.Now().UnixMilli() {
		return empty, contract.ErrUnavailable
	}
	o := c.options
	scope := c.checkpointScope()
	next, revision := o.StartMicros, int64(0)
	current, err := c.Checkpoint(ctx)
	if err != nil {
		return empty, err
	}
	if current != nil {
		if current.MappingDigest != c.mapping {
			return empty, contract.ErrConflict
		}
		next, revision = current.NextMicros, current.MappingRevision
	}
	if next <= coverage.RetainedFromMicros {
		return empty, contract.ErrUnavailable
	}
	if next >= coverage.VisibleBeforeMicros {
		return IdentityCollection{NextMicros: next}, nil
	}
	upper := min(next+60000000, coverage.VisibleBeforeMicros)
	instant := func(v int64) string { return time.UnixMicro(v).UTC().Format("2006-01-02T15:04:05.000000Z") }
	ctx, cancelOperation := context.WithTimeout(ctx, 30*time.Second)
	defer cancelOperation()
	var selected []provisioning.IdentityEvent
	for {
		if coverage.ValidUntilMs <= time.Now().UnixMilli() {
			return empty, contract.ErrUnavailable
		}
		scan, err := c.reader.ReadWindow(ctx, instant(next-1), instant(upper))
		if err != nil {
			return empty, err
		}
		selected = selected[:0]
		for _, event := range scan.Events {
			if c.allowed[IdentityAggregate{event.AggregateType, event.AggregateID}] {
				selected = append(selected, event)
			}
		}
		if len(selected) <= 256 {
			break
		}
		if upper-next == 1 {
			return empty, contract.ErrUnavailable
		}
		upper = next + (upper-next)/2
	}
	if coverage.ValidUntilMs <= time.Now().UnixMilli() {
		return empty, contract.ErrUnavailable
	}
	events := make([]map[string]any, 0, len(selected))
	recordedAt := float64(time.Now().UnixMilli())
	for _, event := range selected {
		identity, err := json.Marshal([]string{o.Issuer, o.OrganizationID, o.TenantID, event.AggregateType, event.AggregateID, event.Sequence})
		if err != nil {
			return empty, contract.ErrUnavailable
		}
		digest := sha256.Sum256(identity)
		id := hex.EncodeToString(digest[:])
		occurred, err := time.Parse(time.RFC3339Nano, event.CreationDate)
		if err != nil {
			return empty, contract.ErrUnavailable
		}
		events = append(events, map[string]any{
			"schemaVersion": float64(1), "eventId": id, "eventType": "heimdall.identity.observed", "eventVersion": float64(1),
			"occurredAt": float64(occurred.UnixMilli()), "recordedAt": recordedAt,
			"productId": o.ProductID, "environment": o.Environment, "tenantId": o.TenantID,
			"actor": map[string]any{"kind": "unknown"}, "target": map[string]any{"type": "zitadel_aggregate", "id": event.AggregateID},
			"action": event.Type, "outcome": "succeeded", "requestId": id, "source": "zitadel", "sdkVersion": "0.0.0",
			"attributes": map[string]any{"issuer": o.Issuer, "organizationId": o.OrganizationID, "aggregateType": event.AggregateType, "sequence": event.Sequence, "creationDate": event.CreationDate},
		})
	}
	err = c.audit.AppendIdentityWindow(ctx, events, func(ctx context.Context, tx *sql.Tx) error {
		if coverage.ValidUntilMs <= time.Now().UnixMilli() {
			return contract.ErrUnavailable
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO heimdall_directory.event_checkpoints(product_id,environment,issuer,organization_id,tenant_id,next_micros,mapping_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", append(scope, o.StartMicros, c.mapping)...)
		if err != nil {
			return identityCollectionDBError(ctx)
		}
		result, err := tx.ExecContext(ctx, "UPDATE heimdall_directory.event_checkpoints SET next_micros=$7 WHERE "+identityCheckpointScope+" AND next_micros=$6 AND mapping_digest=$8 AND mapping_revision=$9", append(scope, next, upper, c.mapping, revision)...)
		if err != nil {
			return identityCollectionDBError(ctx)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return contract.ErrUnavailable
		}
		if count != 1 {
			return contract.ErrConflict
		}
		return nil
	})
	if err != nil {
		return empty, err
	}
	return IdentityCollection{NextMicros: upper, Recorded: len(events)}, nil
}

func identityCollectionDBError(ctx context.Context) error {
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	return contract.ErrUnavailable
}
