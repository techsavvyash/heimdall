package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

func TestAuditInvalid(t *testing.T) {
	if _, err := NewAudit(nil, "ledger", "production", nil, nil); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
}
func TestAudit(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("live audit")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := sql.Open("postgres", url+"&options=-c%20role%3D"+os.Getenv("HEIMDALL_TEST_AUDIT_ROLE"))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	store, err := NewAudit(runtime, "ledger", "production", []string{"primary", "secondary"}, validate)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	event := fixtures[0].Event
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		receipt, err := store.Append(ctx, event)
		if err != nil || receipt.PayloadDigest != fixtures[0].Digest {
			t.Fatal("TS receipt", err)
		}
		return
	}
	rawUnicode, err := os.ReadFile("../../../../contracts/fixtures/audit-storage-unicode.json")
	if err != nil {
		t.Fatal(err)
	}
	var unicode map[string]any
	if json.Unmarshal(rawUnicode, &unicode) != nil {
		t.Fatal("unicode fixture")
	}
	if receipt, err := store.Append(ctx, unicode); err != nil || receipt.EventID != "unicode-nul-shared" {
		t.Fatal("unicode storage parity", err)
	}
	receipt, err := store.Append(ctx, event)
	if err != nil || receipt.PayloadDigest != fixtures[0].Digest {
		t.Fatal("shared receipt", err)
	}
	event["eventId"] = "go-audit-shared"
	if _, err = store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	event["eventId"] = "go-audit-race"
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Append(ctx, event); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(table, id string) int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT count(*) FROM heimdall."+table+" WHERE event_id=$1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("audit_deliveries", "go-audit-race") != 2 {
		t.Fatal("duplicate deliveries")
	}
	event["action"] = "different"
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("conflict", err)
	}
	event["action"] = "invoice.approve"
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.Append(cancelled, event); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal("cancelled", err)
	}
	if _, err = db.Exec("ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT go_test_failure CHECK(sink_id<>'z-go-blocked')"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT go_test_failure")
	failing, _ := NewAudit(runtime, "ledger", "production", []string{"a-good", "z-go-blocked"}, validate)
	event["eventId"] = "go-audit-rollback"
	if _, err = failing.Append(ctx, event); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("rollback", err)
	}
	if count("audit_events", "go-audit-rollback") != 0 || count("audit_deliveries", "go-audit-rollback") != 0 {
		t.Fatal("partial append")
	}
	event["eventId"] = "go-audit-race"
	if _, err = db.Exec("UPDATE heimdall.audit_events SET event=jsonb_set(event,'{action}','\"tampered\"') WHERE event_id='go-audit-race'"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrAuditUnavailable) {
		t.Fatal("corruption", err)
	}
}

type auditFixture struct {
	Event  map[string]any
	Digest string
}

func auditFixtureValidator(t *testing.T) ([]auditFixture, func(map[string]any) error) {
	t.Helper()
	read := func(path string, target any) {
		t.Helper()
		raw, err := os.ReadFile("../../../../contracts/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	var fixtures []auditFixture
	read("fixtures/audit-digests-v1.json", &fixtures)
	var schema any
	read("audit-event.schema.json", &schema)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://test/audit", schema); err != nil {
		t.Fatal(err)
	}
	shape, err := compiler.Compile("https://test/audit")
	if err != nil {
		t.Fatal(err)
	}
	validate := func(event map[string]any) error {
		if shape.Validate(event) != nil {
			return contract.ErrInvalidArgument
		}
		raw, _ := json.Marshal(event)
		var e contract.AuditEvent
		if json.Unmarshal(raw, &e) != nil {
			return contract.ErrInvalidArgument
		}
		return contract.ValidateAuditEvent(e, "sdk", func(string, int, map[string]any) bool { return false })
	}

	return fixtures, validate
}
func TestAuditCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_AUDIT_COMMIT_LOSS")
	if url == "" {
		t.Skip("commit response loss proxy")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	direct, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	event := fixtures[0].Event
	event["eventId"] = "go-audit-lost-commit"
	ctx := context.Background()
	store, err := NewAudit(db, "ledger", "production", []string{"primary", "secondary"}, validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost response", err)
	}
	var count int
	if err = direct.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id=$1", event["eventId"]).Scan(&count); err != nil || count != 2 {
		t.Fatal("committed deliveries", err)
	}
	recovered, _ := NewAudit(direct, "ledger", "production", []string{"new-sink"}, validate)
	if _, err = recovered.Append(ctx, event); err != nil {
		t.Fatal("recovered receipt", err)
	}
	if err = direct.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id=$1", event["eventId"]).Scan(&count); err != nil || count != 2 {
		t.Fatal("changed routing", err)
	}
}

func TestAuditAcknowledgements(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live acknowledgements")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deliveryDB, err := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_audit_delivery")
	if err != nil {
		t.Fatal(err)
	}
	defer deliveryDB.Close()
	writer, _ := NewAudit(db, "ledger", "production", []string{"siem", "other-sink"}, validate)
	store, _ := NewAudit(deliveryDB, "ledger", "production", nil, validate)
	ctx := context.Background()
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-delivery-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name           string
		Batch, Receipt contract.AuditDelivery
		Valid          bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	for index, item := range cases {
		prefix := fmt.Sprintf("go-ack-%d-", index)
		ids := []string{prefix + "e1", prefix + "e2"}
		for _, id := range ids {
			event := fixtures[0].Event
			event["eventId"] = id
			if _, err = writer.Append(ctx, event); err != nil {
				t.Fatal(err)
			}
		}
		for i, id := range item.Batch.EventIDs {
			item.Batch.EventIDs[i] = prefix + id
		}
		for i, id := range item.Receipt.EventIDs {
			item.Receipt.EventIDs[i] = prefix + id
		}
		err = store.Acknowledge(ctx, item.Batch, item.Receipt, 1000)
		if (err == nil) != item.Valid {
			t.Fatalf("%s: %v", item.Name, err)
		}
		for _, id := range ids {
			for _, sink := range []string{"siem", "other-sink"} {
				want := "pending"
				if item.Valid && sink == "siem" {
					for _, acked := range item.Receipt.EventIDs {
						if acked == id {
							want = "delivered"
						}
					}
				}
				var state string
				if err = db.QueryRow("SELECT state FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id=$2", id, sink).Scan(&state); err != nil || state != want {
					t.Fatalf("%s state %s want %s: %v", item.Name, state, want, err)
				}
			}
		}
	}
	ids := []string{"go-ack-atomic-1", "go-ack-atomic-2"}
	for _, id := range ids {
		fixtures[0].Event["eventId"] = id
		if _, err = writer.Append(ctx, fixtures[0].Event); err != nil {
			t.Fatal(err)
		}
	}
	batch := contract.AuditDelivery{ProductID: "ledger", Environment: "production", SinkID: "siem", EventIDs: ids}
	if _, err = db.Exec("UPDATE heimdall.audit_deliveries SET state='quarantined' WHERE event_id=$1 AND sink_id='siem'", ids[1]); err != nil {
		t.Fatal(err)
	}
	if err = store.Acknowledge(ctx, batch, batch, 2000); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id='siem'", ids[0]).Scan(&state); err != nil || state != "pending" {
		t.Fatal("partial update", err)
	}
	missing := batch
	missing.EventIDs = []string{ids[0], "missing-route"}
	if err = store.Acknowledge(ctx, missing, missing, 2000); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE heimdall.audit_deliveries SET state='pending' WHERE event_id=$1 AND sink_id='siem'", ids[1]); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.Acknowledge(ctx, batch, batch, 3000) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Acknowledge(ctx, batch, batch, 4000); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		var at int64
		if err = db.QueryRow("SELECT delivered_at FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id='siem'", id).Scan(&at); err != nil || at != 3000 {
			t.Fatal("replay changed timestamp", err)
		}
	}
}

func TestAcknowledgementCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_ACK_COMMIT_LOSS")
	if url == "" {
		t.Skip("ack response loss proxy")
	}
	fixtures, validate := auditFixtureValidator(t)
	fault, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	direct, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	writer, _ := NewAudit(direct, "ledger", "production", []string{"siem"}, validate)
	store, _ := NewAudit(fault, "ledger", "production", nil, validate)
	ctx := context.Background()
	ids := []string{"go-ack-loss-1", "go-ack-loss-2"}
	for _, id := range ids {
		fixtures[0].Event["eventId"] = id
		if _, err = writer.Append(ctx, fixtures[0].Event); err != nil {
			t.Fatal(err)
		}
	}
	batch := contract.AuditDelivery{ProductID: "ledger", Environment: "production", SinkID: "siem", EventIDs: ids}
	receipt := batch
	receipt.EventIDs = []string{ids[0]}
	if err = store.Acknowledge(ctx, batch, receipt, 1234); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost ack response", err)
	}
	verify := func() {
		t.Helper()
		for i, id := range ids {
			var state string
			var at sql.NullInt64
			if err = direct.QueryRow("SELECT state,delivered_at FROM heimdall.audit_deliveries WHERE event_id=$1", id).Scan(&state, &at); err != nil {
				t.Fatal(err)
			}
			if i == 0 && (state != "delivered" || !at.Valid || at.Int64 != 1234) || i == 1 && (state != "pending" || at.Valid) {
				t.Fatal("unexpected acknowledgement state", state, at)
			}
		}
	}
	verify()
	if err = writer.Acknowledge(ctx, batch, receipt, 2345); err != nil {
		t.Fatal(err)
	}
	verify()
}

func TestAuditPending(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("live pending selection")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	shared, _ := NewAudit(db, "ledger", "production", []string{"pending-shared"}, validate)
	existing, err := shared.Pending(ctx, "pending-shared", 100, 100)
	if err != nil || len(existing) == 0 || existing[0].Event == nil {
		t.Fatal("cross-language pending", err)
	}
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		if len(existing) != 2 {
			t.Fatal("peer read", len(existing))
		}
		return
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-pending.json")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Now     int64
		Records []struct {
			ID                                                      string
			Due                                                     int64
			Attempts                                                int
			State, Product, Environment, Sink, Corruption, TenantID string
		}
		Expected []struct {
			ID       string
			Attempts int
			Valid    bool
		}
	}
	if json.Unmarshal(raw, &matrix) != nil {
		t.Fatal("fixture")
	}
	clone := func(product, environment, id string) map[string]any {
		raw, _ := json.Marshal(fixtures[0].Event)
		var event map[string]any
		json.Unmarshal(raw, &event)
		event["productId"] = product
		event["environment"] = environment
		event["eventId"] = id
		return event
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range matrix.Records {
		product := "ledger-pending-go"
		if row.Product != "" {
			product = "support-pending-go"
		}
		environment := row.Environment
		if environment == "" {
			environment = "production"
		}
		sink := row.Sink
		if sink == "" {
			sink = "pending"
		}
		state := row.State
		if state == "" {
			state = "pending"
		}
		event := clone(product, environment, "go-pending-"+row.ID)
		if row.TenantID != "" {
			event["tenantId"] = row.TenantID
		}
		writer, _ := NewAudit(db, product, environment, []string{sink}, validate)
		if _, err = writer.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
		exec("UPDATE heimdall.audit_deliveries SET next_attempt_at=$2,attempts=$3,state=$4,delivered_at=CASE WHEN $4='delivered' THEN 100 ELSE NULL END WHERE event_id=$1", event["eventId"], row.Due, row.Attempts, state)
		if row.Corruption == "digest" {
			exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE event_id=$1", event["eventId"])
		}
		if row.Corruption == "payload-scope" {
			event["productId"] = "support-pending-go"
			raw, _ := json.Marshal(event)
			digest, _ := contract.AuditDigest(event)
			exec("UPDATE heimdall.audit_events SET event=jsonb_set(event,'{payload}',to_jsonb($2::text)),payload_digest=decode($3,'hex') WHERE event_id=$1", event["eventId"], base64.RawURLEncoding.EncodeToString(raw), digest)
		}
	}
	store, _ := NewAudit(db, "ledger-pending-go", "production", nil, validate)
	selected, err := store.Pending(ctx, "pending", matrix.Now, 100)
	if err != nil || len(selected) != len(matrix.Expected) {
		t.Fatal("selection", err)
	}
	for i, want := range matrix.Expected {
		got := selected[i]
		if got.EventID != "go-pending-"+want.ID || got.Attempts != want.Attempts || (got.Event != nil) != want.Valid {
			t.Fatal("selection mismatch", got.EventID)
		}
	}
	limited, err := store.Pending(ctx, "pending", matrix.Now, 2)
	if err != nil || len(limited) != 2 || limited[0].EventID != "go-pending-a" || limited[1].EventID != "go-pending-b" {
		t.Fatal("limit", err)
	}
	repeated, err := store.Pending(ctx, "pending", matrix.Now, 100)
	if err != nil || !reflect.DeepEqual(repeated, selected) {
		t.Fatal("read changed state", err)
	}
	if _, err = store.Pending(ctx, "pending", matrix.Now, 0); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.Pending(cancelled, "pending", matrix.Now, 100); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
	budget, _ := NewAudit(db, "budget-go", "production", []string{"budget"}, validate)
	for i := 0; i < 6; i++ {
		if _, err = budget.Append(ctx, clone("budget-go", "production", fmt.Sprintf("budget-go-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE heimdall.audit_events SET event=jsonb_set(event,'{payload}',to_jsonb(repeat('a',1000000))) WHERE product_id='budget-go'")
	bounded, err := budget.Pending(ctx, "budget", 100, 100)
	if err != nil || len(bounded) != 4 {
		t.Fatal("byte budget", err)
	}
	for _, item := range bounded {
		if item.Event != nil {
			t.Fatal("exported invalid payload")
		}
	}
	if _, err = shared.Append(ctx, clone("ledger", "production", "pending-go-shared")); err != nil {
		t.Fatal(err)
	}
}

func TestAuditTransitions(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live transitions")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deliveryDB, err := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_audit_delivery")
	if err != nil {
		t.Fatal(err)
	}
	defer deliveryDB.Close()
	writer, _ := NewAudit(db, "retry-go", "production", []string{"retry", "other"}, validate)
	store, _ := NewAudit(deliveryDB, "retry-go", "production", nil, validate)
	ctx := context.Background()
	seed := func(id string) {
		t.Helper()
		event := fixtures[0].Event
		event["productId"] = "retry-go"
		event["eventId"] = id
		if _, err := writer.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-transitions.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Operation      string
		Attempt        int
		Now, RetryAt   int64
		Result         any
		StoredAttempts int
		StoredDue      int64
		StoredState    string
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	seed("retry-go-main")
	for _, step := range cases {
		var result any
		var err error
		switch step.Operation {
		case "begin":
			var claim *int
			claim, err = store.BeginAttempt(ctx, "retry", "retry-go-main", step.Attempt, step.Now, step.RetryAt)
			if claim != nil {
				result = float64(*claim)
			}
		case "defer":
			result, err = store.DeferAttempt(ctx, "retry", "retry-go-main", step.Attempt, step.RetryAt)
		case "quarantine":
			result, err = store.Quarantine(ctx, "retry", "retry-go-main", step.Attempt)
		}
		if err != nil || !reflect.DeepEqual(result, step.Result) {
			t.Fatal("transition", step.Operation, result, err)
		}
		var attempt int
		var due int64
		var state string
		if err = db.QueryRow("SELECT attempts,next_attempt_at,state FROM heimdall.audit_deliveries WHERE product_id='retry-go' AND event_id='retry-go-main' AND sink_id='retry'").Scan(&attempt, &due, &state); err != nil || attempt != step.StoredAttempts || due != step.StoredDue || state != step.StoredState {
			t.Fatal("stored transition", err)
		}
	}
	var attempt int
	var state string
	if err = db.QueryRow("SELECT attempts,state FROM heimdall.audit_deliveries WHERE product_id='retry-go' AND event_id='retry-go-main' AND sink_id='other'").Scan(&attempt, &state); err != nil || attempt != 0 || state != "pending" {
		t.Fatal("sink isolation", err)
	}
	other, _ := NewAudit(deliveryDB, "support", "production", nil, validate)
	if claim, err := other.BeginAttempt(ctx, "retry", "retry-go-main", 0, 100, 200); err != nil || claim != nil {
		t.Fatal("product isolation", err)
	}
	if claim, err := store.BeginAttempt(ctx, "missing", "retry-go-main", 0, 100, 200); err != nil || claim != nil {
		t.Fatal("sink missing", err)
	}
	seed("retry-go-race")
	var wg sync.WaitGroup
	claims := make(chan *int, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := store.BeginAttempt(ctx, "retry", "retry-go-race", 0, 100, 200)
			claims <- claim
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	winners := 0
	for claim := range claims {
		if claim != nil {
			winners++
			if *claim != 1 {
				t.Fatal("counter")
			}
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("claim race", winners)
	}
	batch := contract.AuditDelivery{ProductID: "retry-go", Environment: "production", SinkID: "retry", EventIDs: []string{"retry-go-race"}}
	if err = store.Acknowledge(ctx, batch, batch, 150); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.DeferAttempt(ctx, "retry", "retry-go-race", 1, 999); err != nil || changed {
		t.Fatal("delivered defer", err)
	}
	if changed, err := store.Quarantine(ctx, "retry", "retry-go-race", 1); err != nil || changed {
		t.Fatal("delivered quarantine", err)
	}
	if claim, err := store.BeginAttempt(ctx, "retry", "retry-go-race", 1, 999, 1000); err != nil || claim != nil {
		t.Fatal("delivered claim", err)
	}
	if _, err = store.BeginAttempt(ctx, "retry", "unused", 2147483647, 100, 200); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	seed("retry-go-0-invalid")
	seed("retry-go-1-healthy")
	if _, err = db.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE event_id='retry-go-0-invalid'"); err != nil {
		t.Fatal(err)
	}
	first, err := writer.Pending(ctx, "retry", 100, 1)
	if err != nil || len(first) != 1 || first[0].Event != nil {
		t.Fatal("invalid head", err)
	}
	if changed, err := store.Quarantine(ctx, "retry", "retry-go-0-invalid", 0); err != nil || !changed {
		t.Fatal(err)
	}
	next, err := writer.Pending(ctx, "retry", 100, 1)
	if err != nil || len(next) != 1 || next[0].EventID != "retry-go-1-healthy" || next[0].Event == nil {
		t.Fatal("poison progress", err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE event_id='retry-go-0-invalid'").Scan(&count); err != nil || count != 1 {
		t.Fatal("quarantine deleted event", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.BeginAttempt(cancelled, "retry", "retry-go-1-healthy", 0, 100, 200); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
}

func TestTransitionCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_TRANSITION_LOSS")
	if url == "" {
		t.Skip("transition response loss proxy")
	}
	fixtures, validate := auditFixtureValidator(t)
	fault, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	direct, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	operation := os.Getenv("HEIMDALL_TEST_TRANSITION_KIND")
	id := "transition-go-loss-" + operation
	ctx := context.Background()
	writer, _ := NewAudit(direct, "ledger", "production", []string{"transition-loss"}, validate)
	store, _ := NewAudit(fault, "ledger", "production", nil, validate)
	fixtures[0].Event["eventId"] = id
	if _, err = writer.Append(ctx, fixtures[0].Event); err != nil {
		t.Fatal(err)
	}
	if operation != "begin" {
		if _, err = writer.BeginAttempt(ctx, "transition-loss", id, 0, 100, 200); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(target *Audit) (any, error) {
		switch operation {
		case "begin":
			claim, err := target.BeginAttempt(ctx, "transition-loss", id, 0, 100, 200)
			if claim == nil {
				return nil, err
			}
			return *claim, err
		case "defer":
			return target.DeferAttempt(ctx, "transition-loss", id, 1, 300)
		default:
			return target.Quarantine(ctx, "transition-loss", id, 1)
		}
	}
	if _, err = invoke(store); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost transition response", err)
	}
	expectedDue := int64(200)
	expectedState := "pending"
	var expectedResult any
	if operation == "defer" {
		expectedDue = 300
	}
	if operation == "quarantine" {
		expectedState = "quarantined"
	}
	if operation != "begin" {
		expectedResult = true
	}
	verify := func() {
		t.Helper()
		var attempt int
		var due int64
		var state string
		if err = direct.QueryRow("SELECT attempts,next_attempt_at,state FROM heimdall.audit_deliveries WHERE event_id=$1", id).Scan(&attempt, &due, &state); err != nil || attempt != 1 || due != expectedDue || state != expectedState {
			t.Fatal("stored transition", err)
		}
	}
	verify()
	result, err := invoke(writer)
	if err != nil || !reflect.DeepEqual(result, expectedResult) {
		t.Fatal("transition replay", result, err)
	}
	verify()
}

func TestAuditedDecision(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live decision")
	}
	f, validate := auditFixtureValidator(t)
	db, e := sql.Open("postgres", url+"&options=-c%20role%3D"+os.Getenv("HEIMDALL_TEST_AUDIT_ROLE"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store, e := NewAudit(db, "ledger", "auditflow-go", []string{"primary"}, validate)
	if e != nil {
		t.Fatal(e)
	}
	var ids []string
	runner, e := auditflow.New("ledger", "auditflow-go", func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		r, e := store.Append(ctx, event)
		if e == nil {
			ids = append(ids, r.EventID)
		}
		return r, e
	}, validate, nil)
	if e != nil {
		t.Fatal(e)
	}
	f[0].Event["environment"] = "auditflow-go"
	for _, allow := range []bool{true, false} {
		got, e := runner.Run(context.Background(), f[0].Event, func(ctx context.Context) (bool, error) {
			var count int
			if e := db.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.audit_events WHERE event_id=$1", ids[len(ids)-1]).Scan(&count); e != nil || count != 1 {
				t.Error("requested not committed", e)
			}
			return allow, nil
		})
		if e != nil || got != allow {
			t.Fatal("decision", e)
		}
		var count int
		if e = db.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id=$1", ids[len(ids)-1]).Scan(&count); e != nil || count != 1 {
			t.Fatal("terminal not committed", e)
		}
	}
}
func TestDecisionCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DECISION_COMMIT_LOSS")
	if url == "" {
		t.Skip("decision commit loss")
	}
	f, validate := auditFixtureValidator(t)
	fault, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	direct, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer direct.Close()
	normal, e := NewAudit(direct, "ledger", "decision-loss-go", []string{"primary"}, validate)
	if e != nil {
		t.Fatal(e)
	}
	broken, e := NewAudit(fault, "ledger", "decision-loss-go", []string{"primary"}, validate)
	if e != nil {
		t.Fatal(e)
	}
	stage, _ := strconv.Atoi(os.Getenv("HEIMDALL_TEST_DECISION_LOSS_STAGE"))
	events := []map[string]any{}
	var eventsMu sync.Mutex
	evaluated := false
	runner, e := auditflow.New("ledger", "decision-loss-go", func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		eventsMu.Lock()
		events = append(events, event)
		count := len(events)
		eventsMu.Unlock()
		if count == stage {
			return broken.Append(ctx, event)
		}
		return normal.Append(ctx, event)
	}, validate, nil)
	if e != nil {
		t.Fatal(e)
	}
	f[0].Event["environment"] = "decision-loss-go"
	_, e = runner.Run(context.Background(), f[0].Event, func(context.Context) (bool, error) { evaluated = true; return true, nil })
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if !errors.Is(e, contract.ErrAuditUnavailable) || evaluated != (stage == 2) || len(events) != stage {
		t.Fatal("released uncertain decision", e)
	}
	for _, event := range events {
		var count int
		if e = direct.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE event_id=$1", event["eventId"]).Scan(&count); e != nil || count != 1 {
			t.Fatal("commit missing", e)
		}
		if _, e = normal.Append(context.Background(), event); e != nil {
			t.Fatal("exact append retry failed", e)
		}
	}
}
