// heimdall-keys is deployment tooling for the shared record-key ledger.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("heimdall-keys", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // Never echo supplied arguments or secret paths.
	initialize := flags.Bool("initialize", false, "install checksum-pinned key usage schema")
	status := flags.Bool("status", false, "verify key schema compatibility without mutation")
	register := flags.Bool("register", false, "register previously unused key material")
	retire := flags.Bool("retire", false, "irreversibly retire a key fingerprint")
	sqlPath := flags.String("sql", "", "path to shared keys/0001.sql")
	keyPath := flags.String("key-file", "", "private regular file containing exactly 32 raw key bytes")
	fingerprint := flags.String("fingerprint", "", "lowercase SHA-256 ledger fingerprint to retire")
	fresh := flags.Bool("fresh-key", false, "assert previously unseen material has never encrypted records")
	limit := flags.Int64("limit", 0, "immutable encryption allowance, 1 through 4294967295")
	if parseErr := flags.Parse(args); parseErr != nil {
		if parseErr == flag.ErrHelp {
			flags.SetOutput(out)
			fmt.Fprintln(out, "Usage: heimdall-keys [operation] [options]")
			flags.PrintDefaults()
			return 0
		}
		fmt.Fprintln(diagnostic, "invalid arguments")
		return 2
	}
	modes := 0
	for _, selected := range []bool{*initialize, *status, *register, *retire} {
		if selected {
			modes++
		}
	}
	dsn := os.Getenv("HEIMDALL_KEY_DATABASE_URL")
	invalid := modes != 1 || flags.NArg() != 0 || dsn == ""
	if *initialize {
		invalid = invalid || *sqlPath == ""
	} else {
		invalid = invalid || *sqlPath != ""
	}
	if *register {
		invalid = invalid || *keyPath == "" || !*fresh || *limit < 1 || *limit > contract.MaxRecordKeyEncryptions
	} else {
		invalid = invalid || *keyPath != "" || *fresh || *limit != 0
	}
	if *retire {
		invalid = invalid || contract.ValidateKeyUsage(*fingerprint, 1) != nil
	} else {
		invalid = invalid || *fingerprint != ""
	}
	if invalid {
		fmt.Fprintln(diagnostic, "select one key operation with its required inputs and HEIMDALL_KEY_DATABASE_URL")
		return 2
	}
	var migration []byte
	if *initialize {
		var err error
		migration, err = os.ReadFile(*sqlPath)
		if err != nil {
			fmt.Fprintln(diagnostic, "cannot read shared migration")
			return 1
		}
	}
	if *register {
		key, err := readKey(*keyPath)
		if err != nil {
			fmt.Fprintln(diagnostic, "key file must be private, regular and contain exactly 32 raw bytes")
			return 1
		}
		value, err := contract.RecordKeyFingerprint(key)
		clear(key)
		if err != nil {
			fmt.Fprintln(diagnostic, "invalid key material")
			return 1
		}
		*fingerprint = value
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(diagnostic, "invalid database configuration")
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch {
	case *initialize:
		err = postgres.MigrateKeyUsage(ctx, db, migration)
	case *status:
		err = postgres.CheckKeyUsageReadiness(ctx, db)
	case *register:
		err = postgres.RegisterRecordKeyUsage(ctx, db, *fingerprint, *limit, *fresh)
	case *retire:
		err = postgres.RetireRecordKeyUsage(ctx, db, *fingerprint)
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	switch {
	case *register:
		fmt.Fprintln(out, "key registered", *fingerprint)
	case *retire:
		fmt.Fprintln(out, "key retired", *fingerprint)
	default:
		fmt.Fprintln(out, "key usage schema v1 ready")
	}
	return 0
}
func readKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, contract.ErrInvalidArgument
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() != 32 {
		return nil, contract.ErrInvalidArgument
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, contract.ErrInvalidArgument
	}
	return key, nil
}
