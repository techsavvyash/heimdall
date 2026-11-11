// heimdall-migrate is deployment tooling, not a running service.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func main() { os.Exit(run()) }
func run() int {
	sqlPath := flag.String("sql", "", "path to the checksum-pinned selected migration 0001.sql")
	initialize := flag.Bool("initialize-session-schema", false, "explicitly authorize installation in the selected application database")
	managed := flag.Bool("initialize-managed-schema", false, "install managed schema after verifying session/audit readiness")
	status := flag.String("status", "", "read-only compatibility status: session or managed")
	flag.Parse()
	dsn := os.Getenv("HEIMDALL_MIGRATION_DATABASE_URL")
	modes := 0
	if *initialize {
		modes++
	}
	if *managed {
		modes++
	}
	if *status != "" {
		modes++
	}
	if modes != 1 || dsn == "" || flag.NArg() != 0 || (*status != "" && (*status != "session" && *status != "managed" || *sqlPath != "")) || (*status == "" && *sqlPath == "") {
		fmt.Fprintln(os.Stderr, "select exactly one initialization or status mode, with database configuration and SQL only for initialization")
		return 2
	}
	var migration []byte
	var err error
	if *status == "" {
		migration, err = os.ReadFile(*sqlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read shared migration")
			return 1
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
	fmt.Println(family + " schema v1 ready")
	return 0
}
