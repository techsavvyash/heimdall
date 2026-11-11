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
	status := flag.String("status", "", "read-only compatibility status: session, managed or audit-v2")
	prepare := flag.Bool("prepare-audit-upgrade", false, "prepare audit v2 tables without activating runtime readiness")
	directory := flag.String("sql-directory", "", "shared session SQL directory for explicit audit upgrade preparation")
	drained := flag.Bool("writers-drained", false, "assert all old application writers and retention jobs have been stopped and cannot resume")
	activate := flag.Bool("activate-audit-upgrade", false, "validate all historical bindings and atomically publish audit v2 compatibility")
	backfill := flag.Bool("backfill-audit-upgrade", false, "discover missing scopes or advance one bounded audit backfill page; never activate readiness")
	batch := flag.Int("batch-size", 100, "scope registration or event page bound for audit backfill, 1 to 100")
	flag.Parse()
	batchSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "batch-size" {
			batchSet = true
		}
	})
	dsn := os.Getenv("HEIMDALL_MIGRATION_DATABASE_URL")
	modes := 0
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
	if (*activate && (!*drained || *directory != "" || *sqlPath != "")) || (*backfill && (!*drained || *directory != "" || *sqlPath != "" || *batch < 1 || *batch > 100)) || (!*backfill && batchSet) || modes != 1 || dsn == "" || flag.NArg() != 0 || (*prepare && (*directory == "" || !*drained || *sqlPath != "")) || (!*prepare && !*backfill && !*activate && (*directory != "" || *drained)) || (*status != "" && (*status != "session" && *status != "managed" && *status != "audit-v2" || *sqlPath != "")) || (!*prepare && !*backfill && !*activate && *status == "" && *sqlPath == "") {
		fmt.Fprintln(os.Stderr, "select one initialization, status, drained upgrade preparation, backfill or activation mode with its required inputs")
		return 2
	}
	var migration []byte
	var err error
	if *status == "" && !*prepare && !*backfill && !*activate {
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
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid database configuration")
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	family := "session"
	var step *postgres.AuditUpgradeStep
	switch {
	case *activate:
		err = postgres.ActivateAuditUpgrade(ctx, db, *drained, validateBuiltinAudit)
	case *status == "audit-v2":
		err = postgres.CheckAuditReadiness(ctx, db)
	case *backfill:
		step, err = postgres.AdvanceAuditUpgrade(ctx, db, *batch, *drained, validateBuiltinAudit)
	case *prepare:
		err = postgres.PrepareAuditUpgrade(ctx, db, upgrade[0], upgrade[1], upgrade[2], *drained)
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
	if *backfill {
		if json.NewEncoder(os.Stdout).Encode(step) != nil {
			return 1
		}
	} else if *activate || *status == "audit-v2" {
		fmt.Println("audit schema v2 ready; v1 runtime is incompatible")
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
