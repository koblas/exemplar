# Ledger

Ledger records invoices and payments for the demo organisation. It is a
small HTTP service: `internal/httpserver` routes requests, `internal/payments`
holds the payment logic and `internal/auth` checks sessions.

## Running it

    go run ./cmd/ledger

The service listens on `:8080` and reads its database URL from `LEDGER_DB`.
It recieves no secrets on the command line.
