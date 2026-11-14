// Command audit-products reports product identity drift. It is read-only by
// default; pass --apply only after reviewing the report.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"meupreco/internal/audit"
	"meupreco/internal/config"
	"meupreco/internal/product"
	pstore "meupreco/internal/store/postgres"
)

func main() {
	apply := flag.Bool("apply", false, "repair non-colliding drifted rows")
	failOnDrift := flag.Bool("fail-on-drift", false, "exit non-zero when drift is found")
	flag.Parse()

	dsn, err := config.PostgresURLFromEnv()
	if err != nil {
		fatal("audit-products: %v", err)
	}

	stores, err := pstore.New(pstore.Options{DSN: dsn})
	if err != nil {
		fatal("audit-products: %v", err)
	}

	defer stores.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)

	defer cancel()

	products, err := stores.Products().ListAll(ctx)
	if err != nil {
		fatal("audit-products: list products: %v", err)
	}
	brands, err := stores.Brands().List(ctx)
	if err != nil {
		fatal("audit-products: list brands: %v", err)
	}
	entries := make([]product.BrandEntry, 0, len(brands))
	for _, brand := range brands {
		entries = append(entries, product.BrandEntry{Key: brand.Brand, Display: brand.Display})
	}
	findings := audit.Analyze(products, product.NewBrandDictionaryEntries(entries))

	fmt.Println("id\tname\tcanonical_stem\tproduct_key\tstatus\texpected_stem\texpected_key")
	drift := 0
	for _, finding := range findings {
		if !finding.Drifted() {
			continue
		}
		drift++
		stem := ""
		if finding.Product.CanonicalStem != nil {
			stem = *finding.Product.CanonicalStem
		}
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\t%s\n", finding.Product.ID,
			finding.Product.Name, stem, finding.Product.ProductKey,
			strings.Join(finding.Statuses, ","), finding.ExpectedStem, finding.ExpectedKey)
	}
	fmt.Printf("products=%d drifted=%d\n", len(products), drift)

	if *apply {
		result, err := audit.Apply(ctx, stores, findings)
		if err != nil {
			fatal("audit-products: %v", err)
		}
		fmt.Printf("repaired=%d skipped=%d\n", result.Repaired, len(result.Skipped))
		for _, skip := range result.Skipped {
			fmt.Printf("skipped product %d: %s\n", skip.ID, skip.Reason)
		}
	}
	if *failOnDrift && drift > 0 {
		os.Exit(2)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
