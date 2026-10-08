// Package migrations embeds the numbered SQL migrations so they can run from a
// single binary (no external migrate CLI or local file access required).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
