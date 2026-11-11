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
	sqlPath := flag.String("sql", "", "path to the checksum-pinned shared session migration 0001.sql")
	initialize := flag.Bool("initialize-session-schema", false, "explicitly authorize installation in the selected application database")
	flag.Parse()
	dsn := os.Getenv("HEIMDALL_MIGRATION_DATABASE_URL")
	if !*initialize || *sqlPath == "" || dsn == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "require -initialize-session-schema, -sql and HEIMDALL_MIGRATION_DATABASE_URL")
		return 2
	}
	migration, err := os.ReadFile(*sqlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read shared migration")
		return 1
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
	if err = postgres.MigrateSession(ctx, db, migration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("session schema v1 ready")
	return 0
}
