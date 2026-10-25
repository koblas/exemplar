package calibration

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeReplay passes or fails as told and counts its runs.
type fakeReplay struct {
	pass  bool
	err   error
	verif int
	cat   int
}

func (f *fakeReplay) Verifier(_ context.Context, _ []calibrate.Label, _ *schema.VerifierThreshold) (calibrate.Report, error) {
	f.verif++
	if f.err != nil {
		return calibrate.Report{}, f.err
	}
	return calibrate.Report{Pass: f.pass, KnownWrong: calibrate.Rates{N: 2, Refuted: 2, Rate: 1}, KnownTrue: calibrate.Rates{N: 2}}, nil
}

func (f *fakeReplay) Categorizer(context.Context, []calibrate.PRLabel, *schema.CategorizerThreshold) (calibrate.CategorizerReport, error) {
	f.cat++
	return calibrate.CategorizerReport{Pass: f.pass}, f.err
}

func snapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../../examples/exemplar-config"), "cfg-test")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestCalibrationJob(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := storetest.New(t)
	snap := snapshot(t)
	snap.Global.Providers.Categorizer = nil
	key := calibrate.VerifierKey(snap)
	latest := func() queries.LatestCalibrationRow {
		t.Helper()
		row, err := st.LatestCalibration(ctx, queries.LatestCalibrationParams{Kind: calibrate.KindVerifier, Key: key})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}

	// A failing calibration is recorded and is not a job error.
	f := &fakeReplay{pass: false}
	res, err := (&Job{Store: st, Replay: f}).Run(ctx, snap)
	if err != nil || len(res) != 1 || res[0].Passed || res[0].Skipped || latest().Passed {
		t.Fatalf("failing run = %+v, %v", res, err)
	}
	// A passing one is recorded; the next run with the same key skips.
	f.pass = true
	if res, err := (&Job{Store: st, Replay: f}).Run(ctx, snap); err != nil || !res[0].Passed || !latest().Passed {
		t.Fatalf("passing run = %+v, %v", res, err)
	}
	if res, err := (&Job{Store: st, Replay: f}).Run(ctx, snap); err != nil || !res[0].Skipped || f.verif != 2 {
		t.Fatalf("second run = %+v, %v, replays %d", res, err, f.verif)
	}
	// --force reruns, and a later failure re-gates.
	f.pass = false
	if res, err := (&Job{Store: st, Replay: f, Force: true}).Run(ctx, snap); err != nil || res[0].Skipped || latest().Passed {
		t.Fatalf("forced failing run = %+v, %v", res, err)
	}
	// A replay that cannot run records nothing.
	var n int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM calibration_results`).Scan(&n)
	f.err = errors.New("gateway down")
	if _, err := (&Job{Store: st, Replay: f, Force: true}).Run(ctx, snap); err == nil {
		t.Error("an infrastructure failure was not an error")
	}
	var after int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM calibration_results`).Scan(&after)
	if after != n {
		t.Errorf("rows %d -> %d after a failed replay", n, after)
	}
	// With a categorizer configured it is calibrated and recorded too.
	f.err, f.pass = nil, true
	snap.Global.Providers.Categorizer = &schema.Categorizer{A: schema.ProviderModel{Provider: "claude", Model: "a"}, B: schema.ProviderModel{Provider: "bedrock", Model: "b"}}
	if res, err := (&Job{Store: st, Replay: f}).Run(ctx, snap); err != nil || len(res) != 2 || res[1].Kind != calibrate.KindCategorizer || f.cat != 1 {
		t.Errorf("with a categorizer = %+v, %v", res, err)
	}
}
