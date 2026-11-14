package postgres

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"testing"
)

func TestPreparedRetention(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETENTION_URL")
	if url == "" {
		t.Skip("prepared history required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	tenant := "tenant-1"
	type group struct {
		product, environment, resolution string
		ids                              pq.StringArray
	}
	groups := []group{}
	rows, e := db.Query("SELECT d.product_id,d.environment,d.resolution,array_remove(ARRAY[d.request_event_id,d.terminal_event_id,r.recovery_event_id],NULL) FROM heimdall.audit_decisions d JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id) ORDER BY 1,2,3")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var g group
		if e = rows.Scan(&g.product, &g.environment, &g.resolution, &g.ids); e != nil {
			t.Fatal(e)
		}
		groups = append(groups, g)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	port := func(g group) *Audit { a := backfillPort(t, db, g.product); a.environment = g.environment; return a }
	exec := func(q string) {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	exec("CREATE FUNCTION heimdall_retention.test_refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback' USING ERRCODE='23514'; END $$; CREATE TRIGGER test_refuse BEFORE INSERT ON heimdall_retention.decision_identities FOR EACH ROW EXECUTE FUNCTION heimdall_retention.test_refuse()")
	g := groups[0]
	a := port(g)
	if _, e = a.RetainIdentityGroup(ctx, &tenant, g.resolution, g.ids); e != contract.ErrAuditUnavailable {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM heimdall_retention.event_identities").Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	exec("DROP TRIGGER test_refuse ON heimdall_retention.decision_identities; DROP FUNCTION heimdall_retention.test_refuse()")
	other := "other"
	if _, e = a.RetainIdentityGroup(ctx, &other, g.resolution, g.ids); e != contract.ErrAuditUnavailable {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = a.RetainIdentityGroup(cancelled, &tenant, g.resolution, g.ids); e != contract.ErrCancelled {
		t.Fatal(e)
	}
	var saved string
	if e = db.QueryRow("SELECT encode(payload_digest,'hex') FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3", g.product, g.environment, g.ids[0]).Scan(&saved); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id=$1 AND environment=$2 AND event_id=$3", g.product, g.environment, g.ids[0]); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RetainIdentityGroup(ctx, &tenant, g.resolution, g.ids); e != contract.ErrAuditUnavailable {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_events SET payload_digest=decode($4,'hex') WHERE product_id=$1 AND environment=$2 AND event_id=$3", g.product, g.environment, g.ids[0], saved); e != nil {
		t.Fatal(e)
	}
	copyGroup := func(g group) {
		a := port(g)
		original, e := a.RetainIdentityGroup(ctx, &tenant, g.resolution, g.ids)
		if e != nil {
			t.Fatal(e)
		}
		again, e := a.RetainIdentityGroup(ctx, &tenant, g.resolution, g.ids)
		if e != nil || !reflect.DeepEqual(original, again) {
			t.Fatal(e, "retry")
		}
	}
	for _, g := range groups {
		copyGroup(g)
	}
	rows, e = db.Query("SELECT product_id,environment,event_id FROM heimdall.audit_events e WHERE NOT EXISTS(SELECT 1 FROM heimdall_retention.event_identities i WHERE (i.product_id,i.environment,i.event_id)=(e.product_id,e.environment,e.event_id))")
	if e != nil {
		t.Fatal(e)
	}
	ordinary := []group{}
	for rows.Next() {
		var g group
		var id string
		if e = rows.Scan(&g.product, &g.environment, &id); e != nil {
			t.Fatal(e)
		}
		g.resolution = "ordinary"
		g.ids = []string{id}
		ordinary = append(ordinary, g)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	for _, g := range ordinary {
		copyGroup(g)
	}
	var events, decisions bool
	if e = db.QueryRow("SELECT (SELECT count(*) FROM heimdall.audit_events)=(SELECT count(*) FROM heimdall_retention.event_identities),(SELECT count(*) FROM heimdall.audit_decisions)=(SELECT count(*) FROM heimdall_retention.decision_identities)").Scan(&events, &decisions); e != nil || !events || !decisions {
		t.Fatal(events, decisions, e)
	}
}
