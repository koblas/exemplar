// Package schemas embeds the JSON Schema files that define Exemplar's
// configuration contracts. They are the source of truth for the Go types in
// internal/schema and for generated Python models.
package schemas

import "embed"

// FS holds every *.schema.json file in this directory.
//
//go:embed *.schema.json
var FS embed.FS
