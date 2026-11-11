// heimdall-migrate is deployment tooling, not a running service.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func main() { os.Exit(run()) }
func run() int {
	sqlPath := flag.String("sql", "", "path to the checksum-pinned selected migration 0001.sql")
	initialize := flag.Bool("initialize-session-schema", false, "explicitly authorize installation in the selected application database")
	managed := flag.Bool("initialize-managed-schema", false, "install managed schema after verifying session/audit readiness")
	status := flag.String("status", "", "read-only compatibility status: session or managed")
	prepare := flag.Bool("prepare-audit-upgrade", false, "prepare audit v2 tables without activating runtime readiness")
	directory := flag.String("sql-directory", "", "shared session SQL directory for explicit audit upgrade preparation")
	drained := flag.Bool("writers-drained", false, "assert all old application writers and retention jobs have been stopped and cannot resume")
	flag.Parse()
	dsn := os.Getenv("HEIMDALL_MIGRATION_DATABASE_URL")
	modes := 0
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
	if modes != 1 || dsn == "" || flag.NArg() != 0 || (*prepare && (*directory == "" || !*drained || *sqlPath != "")) || (!*prepare && (*directory != "" || *drained)) || (*status != "" && (*status != "session" && *status != "managed" || *sqlPath != "")) || (!*prepare && *status == "" && *sqlPath == "") {
		fmt.Fprintln(os.Stderr, "select one initialization, status or explicit drained audit-upgrade preparation mode with its required SQL inputs")
		return 2
	}
	var migration []byte
	var err error
	if *status == "" && !*prepare {
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
	switch {
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
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *prepare {
		fmt.Println("audit upgrade prepared; runtime remains unavailable; backfill and activation still required")
	} else {
		fmt.Println(family + " schema v1 ready")
	}
	return 0
}
