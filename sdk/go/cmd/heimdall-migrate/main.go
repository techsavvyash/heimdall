// heimdall-migrate is deployment tooling, not a running service.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func main() { os.Exit(run()) }
func run() int {
	sqlPath := flag.String("sql", "", "path to the checksum-pinned selected migration 0001.sql")
	initialize := flag.Bool("initialize-session-schema", false, "explicitly authorize installation in the selected application database")
	managed := flag.Bool("initialize-managed-schema", false, "install managed schema after verifying session/audit readiness")
	status := flag.String("status", "", "read-only compatibility status: session, managed, audit-v2, managed-v2, audit-v3 or managed-v3")
	prepare := flag.Bool("prepare-audit-upgrade", false, "prepare audit v2 tables without activating runtime readiness")
	capacity := flag.Bool("prepare-capacity-upgrade", false, "prepare offline audit-capacity ledger without activating runtime readiness")
	retention := flag.Bool("prepare-retention-upgrade", false, "prepare offline retained identities and metadata quota; never activate runtime readiness")
	retentionRows := flag.Int64("retention-rows", 0, "explicit database-wide retained metadata row limit")
	retentionBytes := flag.Int64("retention-bytes", 0, "explicit database-wide retained metadata logical byte limit")
	capacityEvents := flag.Int64("capacity-events", 0, "explicit database-wide audit event limit for capacity preparation")
	capacityBytes := flag.Int64("capacity-bytes", 0, "explicit database-wide encoded envelope byte limit")
	capacityDeliveries := flag.Int64("capacity-deliveries", 0, "explicit database-wide audit delivery row limit")
	directory := flag.String("sql-directory", "", "shared session SQL directory for explicit audit upgrade preparation")
	drained := flag.Bool("writers-drained", false, "assert all old application writers and retention jobs have been stopped and cannot resume")
	activate := flag.Bool("activate-audit-upgrade", false, "validate all historical bindings and atomically publish audit v2 compatibility")
	capacityActivate := flag.Bool("activate-capacity-upgrade", false, "validate accounted history and atomically publish audit v3 compatibility")
	backfill := flag.Bool("backfill-audit-upgrade", false, "discover missing scopes or advance one bounded audit backfill page; never activate readiness")
	capacityBackfill := flag.Bool("backfill-capacity-upgrade", false, "validate and account one bounded historical page; never activate readiness")
	retentionBackfill := flag.Bool("backfill-retention-upgrade", false, "validate and retain one bounded historical group page; never activate readiness")
	batch := flag.Int("batch-size", 100, "scope, event or complete decision-group page bound, 1 to 100")
	flag.Parse()
	activateMode := *activate || *capacityActivate
	backfillMode := *backfill || *capacityBackfill || *retentionBackfill
	batchSet := false
	capacitySet := false
	retentionSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "retention-rows" || f.Name == "retention-bytes" {
			retentionSet = true
		}
		if f.Name == "capacity-events" || f.Name == "capacity-bytes" || f.Name == "capacity-deliveries" {
			capacitySet = true
		}
		if f.Name == "batch-size" {
			batchSet = true
		}
	})
	dsn := os.Getenv("HEIMDALL_MIGRATION_DATABASE_URL")
	modes := 0
	if *retentionBackfill {
		modes++
	}
	if *retention {
		modes++
	}
	if *capacityActivate {
		modes++
	}
	if *capacityBackfill {
		modes++
	}
	if *capacity {
		modes++
	}
	if *activate {
		modes++
	}
	if *backfill {
		modes++
	}
	if *prepare {
		modes++
	}
	if *initialize {
		modes++
	}
	if *managed {
		modes++
	}
	if *status != "" {
		modes++
	}
	if (*retention && (*directory == "" || !*drained || *sqlPath != "" || *retentionRows < 1 || *retentionRows > 1099511627775 || *retentionBytes < 8192 || *retentionBytes > 9007199254740991)) || (!*retention && retentionSet) || (*capacity && (*directory == "" || !*drained || *sqlPath != "" || *capacityEvents < 1 || *capacityEvents > 9007199254740991 || *capacityBytes < 1 || *capacityBytes > 9007199254740991 || *capacityDeliveries < 1 || *capacityDeliveries > 9007199254740991)) || (!*capacity && capacitySet) || (activateMode && (!*drained || *directory != "" || *sqlPath != "")) || (backfillMode && (!*drained || *directory != "" || *sqlPath != "" || *batch < 1 || *batch > 100)) || (!backfillMode && batchSet) || modes != 1 || dsn == "" || flag.NArg() != 0 || (*prepare && (*directory == "" || !*drained || *sqlPath != "")) || (!*retention && !*capacity && !*prepare && !backfillMode && !activateMode && (*directory != "" || *drained)) || (*status != "" && (*status != "session" && *status != "managed" && *status != "audit-v2" && *status != "managed-v2" && *status != "audit-v3" && *status != "managed-v3" || *sqlPath != "")) || (!*retention && !*capacity && !*prepare && !backfillMode && !activateMode && *status == "" && *sqlPath == "") {
		fmt.Fprintln(os.Stderr, "select one initialization, status, drained upgrade preparation, backfill or activation mode with its required inputs")
		return 2
	}
	var migration []byte
	var err error
	if *status == "" && !*retention && !*capacity && !*prepare && !backfillMode && !activateMode {
		migration, err = os.ReadFile(*sqlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read shared migration")
			return 1
		}
	}
	var upgrade [3][]byte
	if *prepare {
		for i, name := range []string{"0002.sql", "0002-backfill.sql", "0002-upgrade.sql"} {
			upgrade[i], err = os.ReadFile(filepath.Join(*directory, name))
			if err != nil {
				fmt.Fprintln(os.Stderr, "cannot read shared upgrade SQL")
				return 1
			}
		}
	}
	var capacityUpgrade [2][]byte
	if *capacity {
		for i, name := range []string{"0003-capacity.sql", "0003-upgrade.sql"} {
			capacityUpgrade[i], err = os.ReadFile(filepath.Join(*directory, name))
			if err != nil {
				fmt.Fprintln(os.Stderr, "cannot read shared capacity SQL")
				return 1
			}
		}
	}
	var retentionUpgrade [6][]byte
	if *retention {
		for i, name := range []string{"0004-retention-identities.sql", "0004-retention-holds.sql", "0004-retention-retry-holds.sql", "0004-retention-capacity.sql", "0004-retirement-markers.sql", "0004-retention-upgrade.sql"} {
			retentionUpgrade[i], err = os.ReadFile(filepath.Join(*directory, name))
			if err != nil {
				fmt.Fprintln(os.Stderr, "cannot read shared retention SQL")
				return 1
			}
		}
	}
	connector, err := postgres.NewConnector(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid database configuration")
		return 1
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	family := "session"
	var step *postgres.AuditUpgradeStep
	var capacityStep *postgres.CapacityBackfillProgress
	switch {
	case *retentionBackfill:
		capacityStep, err = postgres.AdvanceAuditRetentionUpgrade(ctx, db, *batch, *drained, validateBuiltinAudit)
	case *retention:
		err = postgres.PrepareAuditRetentionUpgrade(ctx, db, retentionUpgrade, *retentionRows, *retentionBytes, *drained)
	case *capacityActivate:
		err = postgres.ActivateAuditCapacityUpgrade(ctx, db, *drained, validateBuiltinAudit)
	case *status == "audit-v3":
		err = postgres.CheckCapacityReadiness(ctx, db)
	case *status == "managed-v3":
		err = postgres.CheckManagedCapacityReadiness(ctx, db)
	case *capacityBackfill:
		capacityStep, err = postgres.AdvanceAuditCapacityUpgrade(ctx, db, *batch, *drained, validateBuiltinAudit)
	case *capacity:
		err = postgres.PrepareAuditCapacityUpgrade(ctx, db, capacityUpgrade[0], capacityUpgrade[1], contract.AuditCapacity{Events: *capacityEvents, EnvelopeBytes: *capacityBytes, Deliveries: *capacityDeliveries}, *drained)
	case *activate:
		err = postgres.ActivateAuditUpgrade(ctx, db, *drained, validateBuiltinAudit)
	case *status == "audit-v2":
		err = postgres.CheckAuditReadiness(ctx, db)
	case *backfill:
		step, err = postgres.AdvanceAuditUpgrade(ctx, db, *batch, *drained, validateBuiltinAudit)
	case *prepare:
		err = postgres.PrepareAuditUpgrade(ctx, db, upgrade[0], upgrade[1], upgrade[2], *drained)
	case *status == "managed-v2":
		err = postgres.CheckManagedAuditReadiness(ctx, db)
	case *status == "managed":
		family = "managed"
		err = postgres.CheckManagedReadiness(ctx, db)
	case *status == "session":
		err = postgres.CheckReadiness(ctx, db)
	case *managed:
		family = "managed"
		err = postgres.MigrateManaged(ctx, db, migration)
	default:
		err = postgres.MigrateSession(ctx, db, migration)
	}
	if err != nil {
		if *backfill && step != nil {
			_ = json.NewEncoder(os.Stderr).Encode(step)
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *capacityBackfill || *retentionBackfill {
		if json.NewEncoder(os.Stdout).Encode(capacityStep) != nil {
			return 1
		}
	} else if *backfill {
		if json.NewEncoder(os.Stdout).Encode(step) != nil {
			return 1
		}
	} else if *capacityActivate || *status == "audit-v3" {
		fmt.Println("audit schema v3 ready; v1 and v2 runtimes are incompatible")
	} else if *status == "managed-v3" {
		fmt.Println("audit schema v3 and managed schema v1 ready")
	} else if *status == "managed-v2" {
		fmt.Println("audit schema v2 and managed schema v1 ready")
	} else if *activate || *status == "audit-v2" {
		fmt.Println("audit schema v2 ready; v1 runtime is incompatible")
	} else if *retention {
		fmt.Println("retention upgrade prepared; runtime unavailable; identity backfill and activation still required")
	} else if *capacity {
		fmt.Println("capacity upgrade prepared; runtime unavailable; historical accounting and activation still required")
	} else if *prepare {
		fmt.Println("audit upgrade prepared; runtime remains unavailable; backfill and activation still required")
	} else {
		fmt.Println(family + " schema v1 ready")
	}
	return 0
}

// This initial CLI validates reserved built-in events only; custom history is
// refused until a matching product schema registry can be supplied explicitly.
func validateBuiltinAudit(event map[string]any) error {
	if e := contract.ValidateAuditShape(event); e != nil {
		return e
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return contract.ErrInvalidArgument
	}
	var typed contract.AuditEvent
	if json.Unmarshal(raw, &typed) != nil {
		return contract.ErrInvalidArgument
	}
	return contract.ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false })
}
