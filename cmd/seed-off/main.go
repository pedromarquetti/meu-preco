// Command seed-off imports a gzipped JSONL Open Food Facts products dump into
// the products table (see internal/enrich). Only Brazilian GTINs (789|790) are
// kept; the import is idempotent, so re-running the same file is safe.
//
// NOTE: The JSONL file can be fetched from
// https://static.openfoodfacts.org/data/openfoodfacts-products.jsonl.gz   → 302 →
// https://openfoodfacts-ds.s3.eu-west-3.amazonaws.com/openfoodfacts-products.jsonl.gz
// TODO: make an auto scheduled downloader for this
//
// then: 
//	go run ./cmd/seed-off ./openfoodfacts-products.jsonl.gz
//
// The database is named by POSTGRES_USERNAME, POSTGRES_PASSWORD, DATABASE_NAME
// (the same env contract as cmd/migrate and cmd/server).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"meupreco/internal/config"
	"meupreco/internal/enrich"
	pstore "meupreco/internal/store/postgres"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: seed-off <openfoodfacts-products.jsonl.gz>")
		os.Exit(1)
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-off: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	dsn, err := config.PostgresURLFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-off: %v\n", err)
		os.Exit(1)
	}

	st, err := pstore.New(pstore.Options{DSN: dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-off: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	stats, err := enrich.SeedFromOFF(ctx, st.Products(), f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-off: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("lines=%d valid_gtin=%d rows_upserted=%d created=%d skipped=%d\n",
		stats.LinesRead, stats.GTINValid, stats.RowsUpserted, stats.Created, stats.Skipped)
}
