// Package payments holds Ledger's payment logic. Amounts are integer cents.
package payments

import (
	"context"
	"database/sql"
	"fmt"
)

// Store reads and writes payments.
type Store struct {
	DB *sql.DB
}

// Total returns the sum of an invoice's payments in cents.
func (s *Store) Total(ctx context.Context, invoiceID string) (int64, error) {
	var cents int64
	err := s.DB.QueryRowContext(ctx, `SELECT coalesce(sum(amount_cents), 0) FROM payments WHERE invoice_id = $1`, invoiceID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("total of invoice %s: %w", invoiceID, err)
	}
	return cents, nil
}
