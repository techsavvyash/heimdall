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
	scope := []any{o.ProductID, o.Environment, o.Issuer, o.OrganizationID, o.TenantID}
	next := o.StartMicros
	var mapping string
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := c.db.QueryRowContext(readCtx, "SELECT next_micros,mapping_digest FROM heimdall_directory.event_checkpoints WHERE "+identityCheckpointScope, scope...).Scan(&next, &mapping)
	if err != nil && err != sql.ErrNoRows {
		failure := identityCollectionDBError(readCtx)
		cancel()
		return empty, failure
	}
	cancel()
	if err == nil && mapping != c.mapping {
		return empty, contract.ErrConflict
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
		result, err := tx.ExecContext(ctx, "UPDATE heimdall_directory.event_checkpoints SET next_micros=$7 WHERE "+identityCheckpointScope+" AND next_micros=$6 AND mapping_digest=$8", append(scope, next, upper, c.mapping)...)
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
