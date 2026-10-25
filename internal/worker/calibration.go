package worker

import (
	"context"

	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// verifierCalibrated reports whether the installation's latest verifier
// calibration for snap's verifier inputs passed (FR-E10). No store, no
// row or a read error count as not calibrated: the gate fails closed.
func verifierCalibrated(ctx context.Context, st *store.Store, snap *config.Snapshot) bool {
	if st == nil {
		return false
	}
	row, err := st.LatestCalibration(ctx, queries.LatestCalibrationParams{Kind: calibrate.KindVerifier, Key: calibrate.VerifierKey(snap)})
	return err == nil && row.Passed
}
