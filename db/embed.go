// Package db embeds the goose SQL migrations. Typed queries are generated
// from db/queries by sqlc into internal/store/queries (`go generate ./db`).
package db

import "embed"

// Migrations holds the goose migration files.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:generate sqlc generate -f sqlc.yaml
