// Command migrate applies the embedded SQL migrations to the database named by
// POSTGRES_USERNAME, POSTGRES_PASSWORD, DATABASE_NAME (see config). Usage:
//
//	go run ./cmd/migrate up        (or: down | version | redo)
//
// It exists so `make migrate-up`/`make migrate-down` never require a global
// golang-migrate binary.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"meupreco/internal/config"
	"meupreco/migrations"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: migrate <up|down|version|redo> (POSTGRES_USERNAME, POSTGRES_PASSWORD and DATABASE_NAME must be set)")
	}

	dsn, err := config.PostgresURLFromEnv()
	if err != nil {
		fatal("%v", err)
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		fatal("load embedded migrations: %v", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		fatal("open migrate: %v", err)
	}

	switch os.Args[1] {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	case "redo":
		err = m.Steps(-1)
		if err == nil {
			err = m.Steps(1)
		}
	case "version":
		v, dirty, verr := m.Version()
		switch {
		case errors.Is(verr, migrate.ErrNilVersion):
			fmt.Println("no migrations applied")
			return
		case verr != nil:
			err = verr
		default:
			fmt.Printf("version %d (dirty=%v)\n", v, dirty)
		}
	default:
		fatal("unknown command %q", os.Args[1])
	}

	switch {
	case err == nil:
		fmt.Println("ok")
	case errors.Is(err, migrate.ErrNoChange):
		fmt.Println("no change")
	default:
		fatal("migrate: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
