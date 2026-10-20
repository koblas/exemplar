package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Config configures a Driver.
type Config struct {
	// GatewayURL and GatewayToken reach exemplar-gateway.
	GatewayURL   string
	GatewayToken string
	HTTPClient   *http.Client
	// Model is the pinned extractor model (claude-sonnet-5-5).
	// The gateway routes by stage; the model it reports is checked
	// against this pin, and only results from the pinned model are cached.
	Model string
	// RequestsPerBatch caps each Message Batch (default 10,000 requests,
	// i.e. 30,000 comments; the API limit is 100,000).
	RequestsPerBatch int
	// MaxAttempts before a comment stops at error (default 3).
	MaxAttempts int
	MaxTokens   int64
	// Eligible re-checks a comment's repo against the current
	// exemplar-config (repo opt-in and denylist): a repo can leave
	// eligibility after it was mined, and nothing from it may then reach
	// a model. Required.
	Eligible func(repo string) bool
	Logger   *slog.Logger
}

// Driver runs extraction over mined_comments.
type Driver struct {
	cfg     Config
	store   *store.Store
	scanner *redact.Scanner
	client  anthropic.Client
	log     *slog.Logger
}

var (
	errConfig      = errors.New("extract: invalid config")
	errBadCustomID = errors.New("bad custom_id")
)

// New returns a Driver.
func New(cfg Config, st *store.Store, sc *redact.Scanner) (*Driver, error) {
	if cfg.GatewayURL == "" || cfg.GatewayToken == "" {
		return nil, fmt.Errorf("%w: gateway URL and token are required", errConfig)
	}
	if cfg.Eligible == nil {
		return nil, fmt.Errorf("%w: an eligibility check is required", errConfig)
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-5-5"
	}
	if cfg.RequestsPerBatch <= 0 {
		cfg.RequestsPerBatch = 10_000
	}
	cfg.RequestsPerBatch = min(cfg.RequestsPerBatch, 100_000)
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4000
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.GatewayURL),
		option.WithAPIKey(cfg.GatewayToken),
		option.WithHeader(llm.HeaderStage, llm.StageGenerator),
		option.WithHeader(llm.HeaderTier, llm.TierStandard),
		option.WithHeader(llm.HeaderCaller, "learning"),
		option.WithHeader(llm.HeaderDataClasses, "review_comments,diff"),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Driver{cfg: cfg, store: st, scanner: sc, client: anthropic.NewClient(opts...), log: cfg.Logger}, nil
}

// SubmitReport summarizes one Submit call.
type SubmitReport struct {
	// BatchID is the review-comment batch; SeedBatchID the revert-seed
	// batch. Each kind has its own prompt version, so they never share one.
	BatchID     string `json:"batch_id,omitempty"`
	SeedBatchID string `json:"revert_seed_batch_id,omitempty"`
	Model       string `json:"model,omitempty"`
	Submitted   int    `json:"submitted_comments"`
	CacheHits   int    `json:"cache_hits"`
	Unredacted  int    `json:"unredactable"`
	// Ineligible comments are from repos no longer eligible; they are
	// parked as ineligible and not sent. Released are parked comments
	// whose repo is eligible again, back to pending.
	Ineligible int   `json:"ineligible"`
	Released   int64 `json:"released"`
}

// pending releases parked comments whose repo is eligible again, then
// lists up to one batch of pending comments.
func (d *Driver) pending(ctx context.Context, rep *SubmitReport) ([]queries.PendingCommentsRow, error) {
	if err := d.release(ctx, rep); err != nil {
		return nil, err
	}
	rows, err := d.store.PendingComments(ctx, int32(d.cfg.RequestsPerBatch*CommentsPerRequest))
	if err != nil {
		return nil, fmt.Errorf("list pending comments: %w", err)
	}
	return rows, nil
}

// release returns parked comments to pending for repos that are eligible
// again (opted back in, or taken off the denylist).
func (d *Driver) release(ctx context.Context, rep *SubmitReport) error {
	repos, err := d.store.IneligibleCommentRepos(ctx)
	if err != nil {
		return fmt.Errorf("list parked repos: %w", err)
	}
	for _, repo := range repos {
		if !d.cfg.Eligible(repo) {
			continue
		}
		n, err := d.store.ReleaseIneligibleComments(ctx, repo)
		if err != nil {
			return fmt.Errorf("release parked comments of %s: %w", repo, err)
		}
		rep.Released += n
	}
	return nil
}

// Submit sends up to one batch of pending comments per kind (review
// comments, revert seeds). Comments whose input hash is cached are
// finished from the cache without a model call.
func (d *Driver) Submit(ctx context.Context) (*SubmitReport, error) {
	rep := &SubmitReport{}
	rows, err := d.pending(ctx, rep)
	if err != nil {
		return nil, err
	}
	work := map[string][]Comment{}
	hashes := map[int64]string{}
	for _, row := range rows {
		if !d.cfg.Eligible(row.Repo) {
			rep.Ineligible++
			if err := d.store.ParkIneligibleComment(ctx, row.ID); err != nil {
				return nil, fmt.Errorf("park ineligible comment: %w", err)
			}
			continue
		}
		body, hunk := deref(row.Body), deref(row.DiffHunk)
		// Ingest scans too; redacting again here keeps a missed secret
		// from failing the whole batch at the gateway's second scan.
		rb, _, err1 := d.scanner.Redact(body)
		rh, _, err2 := d.scanner.Redact(hunk)
		if err1 != nil || err2 != nil {
			rep.Unredacted++
			if err := d.store.FailComment(ctx, queries.FailCommentParams{ID: row.ID, ExtractError: strPtr("secret could not be redacted")}); err != nil {
				return nil, fmt.Errorf("mark comment failed: %w", err)
			}
			continue
		}
		k := kindOf(row.Source)
		h := InputHash(k.version, d.cfg.Model, rb, rh)
		hashes[row.ID] = h
		cached, err := d.store.GetCachedExtraction(ctx, h)
		switch {
		case err == nil:
			var r Result
			if json.Unmarshal(cached, &r) == nil {
				r.CommentID = row.ID
				if err := d.apply(ctx, r, d.cfg.Model, k.version); err != nil {
					return nil, err
				}
				rep.CacheHits++
				continue
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("get cached extraction: %w", err)
		}
		work[k.name] = append(work[k.name], Comment{ID: row.ID, Path: row.Path, Body: rb, Hunk: rh})
	}
	for _, k := range kinds {
		if len(work[k.name]) == 0 {
			continue
		}
		id, model, err := d.submitBatch(ctx, k, work[k.name], hashes)
		if err != nil {
			return nil, err
		}
		if k.name == revertKind.name {
			rep.SeedBatchID = id
		} else {
			rep.BatchID = id
		}
		rep.Model = model
		rep.Submitted += len(work[k.name])
	}
	return rep, nil
}

// submitBatch sends one kind's comments as a Message Batch and records it.
func (d *Driver) submitBatch(ctx context.Context, k kind, work []Comment, hashes map[int64]string) (string, string, error) {
	params := anthropic.MessageBatchNewParams{}
	ids := make([]int64, 0, len(work))
	for start := 0; start < len(work); start += CommentsPerRequest {
		group := work[start:min(start+CommentsPerRequest, len(work))]
		params.Requests = append(params.Requests, anthropic.MessageBatchNewParamsRequest{
			CustomID: customID(group),
			Params: anthropic.MessageBatchNewParamsRequestParams{
				Model:     d.cfg.Model,
				MaxTokens: d.cfg.MaxTokens,
				System:    []anthropic.TextBlockParam{{Text: k.system, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
				Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(k.render(group)))},
				OutputConfig: anthropic.OutputConfigParam{
					Format: anthropic.JSONOutputFormatParam{Schema: outputSchema},
				},
			},
		})
		for _, c := range group {
			ids = append(ids, c.ID)
		}
	}
	var httpResp *http.Response
	batch, err := d.client.Messages.Batches.New(ctx, params, option.WithResponseInto(&httpResp), option.WithMaxRetries(0))
	if err != nil {
		// Nothing was recorded, so the comments stay pending for the next run.
		return "", "", fmt.Errorf("extract: create %s batch: %w", k.name, err)
	}
	model := d.cfg.Model
	if httpResp != nil && httpResp.Header.Get(llm.HeaderModel) != "" {
		model = httpResp.Header.Get(llm.HeaderModel)
	}
	if model != d.cfg.Model {
		d.log.Warn("gateway routed extraction to a different model than the pin; these results will not be cached",
			"pinned", d.cfg.Model, "routed", model)
	}
	err = d.store.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		if err := q.InsertExtractionBatch(ctx, queries.InsertExtractionBatchParams{
			BatchID: batch.ID, ExtractorVersion: k.version, Model: model, CommentCount: int32(len(ids)),
		}); err != nil {
			return fmt.Errorf("insert extraction batch: %w", err)
		}
		for _, id := range ids {
			if err := q.SetCommentHash(ctx, queries.SetCommentHashParams{ID: id, InputHash: strPtr(hashes[id])}); err != nil {
				return fmt.Errorf("set comment hash: %w", err)
			}
		}
		return q.MarkCommentsInBatch(ctx, queries.MarkCommentsInBatchParams{BatchID: batch.ID, Ids: ids})
	})
	if err != nil {
		// The batch exists but is not recorded: its comments stay pending
		// and will be resubmitted; the orphan batch's results are unused.
		return "", "", fmt.Errorf("extract: record batch %s: %w", batch.ID, err)
	}
	d.log.Info("extraction batch submitted", "kind", k.name, "batch_id", batch.ID, "comments", len(ids), "requests", len(params.Requests))
	return batch.ID, model, nil
}

// CollectReport summarizes one Collect call.
type CollectReport struct {
	Batches []BatchOutcome `json:"batches"`
	Waiting int            `json:"still_processing"`
}

// BatchOutcome is one processed batch.
type BatchOutcome struct {
	BatchID  string  `json:"batch_id"`
	Accepted int     `json:"accepted"`
	Rejected int     `json:"rejected"`
	Returned int     `json:"returned_to_pending"`
	Rate     float64 `json:"acceptance_rate"`
}

// Acceptance band from the calibration target: alert outside it.
const (
	minAcceptance = 0.08
	maxAcceptance = 0.20
	// minJudged is how many judged comments a batch needs before its
	// acceptance rate is meaningful enough to alert on.
	minJudged = 100
)

// Collect processes every submitted batch that has ended.
func (d *Driver) Collect(ctx context.Context) (*CollectReport, error) {
	open, err := d.store.OpenExtractionBatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open extraction batches: %w", err)
	}
	rep := &CollectReport{}
	for _, b := range open {
		mb, err := d.client.Messages.Batches.Get(ctx, b.BatchID, anthropic.MessageBatchGetParams{})
		if err != nil {
			return rep, fmt.Errorf("extract: get batch %s: %w", b.BatchID, err)
		}
		if mb.ProcessingStatus != anthropic.MessageBatchProcessingStatusEnded {
			rep.Waiting++
			continue
		}
		k, ok := kindByVersion(b.ExtractorVersion)
		if !ok {
			// A batch submitted under an older prompt version: its results
			// are still valid for that version, so record them under it.
			k = kind{name: "superseded", version: b.ExtractorVersion}
		}
		out, err := d.collectBatch(ctx, b.BatchID, b.Model, k)
		if err != nil {
			return rep, err
		}
		rep.Batches = append(rep.Batches, *out)
	}
	return rep, nil
}

func (d *Driver) collectBatch(ctx context.Context, batchID, model string, k kind) (*BatchOutcome, error) {
	out := &BatchOutcome{BatchID: batchID}
	inBatch, err := d.store.CommentsInBatch(ctx, &batchID)
	if err != nil {
		return nil, fmt.Errorf("load batch comments: %w", err)
	}
	hashOf := map[int64]string{}
	for _, c := range inBatch {
		hashOf[c.ID] = deref(c.InputHash)
	}
	handled := map[int64]bool{}

	stream := d.client.Messages.Batches.ResultsStreaming(ctx, batchID, anthropic.MessageBatchResultsParams{})
	for stream.Next() {
		cur := stream.Current()
		ids, err := parseCustomID(cur.CustomID)
		if err != nil {
			d.log.Warn("unexpected custom_id in results", "batch_id", batchID, "custom_id", cur.CustomID)
			continue
		}
		results, reason := decodeResult(cur, ids)
		var back []int64
		for _, id := range ids {
			if _, ours := hashOf[id]; !ours {
				continue // already finished elsewhere, or not this batch's
			}
			handled[id] = true
			r, ok := results[id]
			if !ok {
				back = append(back, id)
				continue
			}
			if err := d.apply(ctx, r, model, k.version); err != nil {
				return nil, err
			}
			if err := d.cache(ctx, hashOf[id], model, k.version, r); err != nil {
				return nil, err
			}
			if r.IsReusable {
				out.Accepted++
			} else {
				out.Rejected++
			}
		}
		if len(back) > 0 {
			if err := d.returnToPending(ctx, back, reason); err != nil {
				return nil, err
			}
			out.Returned += len(back)
		}
	}
	if err := stream.Err(); err != nil {
		// Leave the batch open: the next Collect reads it again, and the
		// comments already finished are skipped.
		return nil, fmt.Errorf("extract: read results of %s: %w", batchID, err)
	}
	var missing []int64
	for id := range hashOf {
		if !handled[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		if err := d.returnToPending(ctx, missing, "missing from batch results"); err != nil {
			return nil, err
		}
		out.Returned += len(missing)
	}
	if judged := out.Accepted + out.Rejected; judged > 0 {
		out.Rate = float64(out.Accepted) / float64(judged)
		if k.calibrated && judged >= minJudged && (out.Rate < minAcceptance || out.Rate > maxAcceptance) {
			d.log.Warn("extractor acceptance outside the calibrated band; check the prompt or the input mix",
				"batch_id", batchID, "rate", out.Rate, "band_min", minAcceptance, "band_max", maxAcceptance)
		}
	}
	if err := d.store.FinishExtractionBatch(ctx, queries.FinishExtractionBatchParams{
		BatchID: batchID, Status: "processed",
		Accepted: i32(out.Accepted), Rejected: i32(out.Rejected), Returned: i32(out.Returned),
	}); err != nil {
		return nil, fmt.Errorf("finish extraction batch: %w", err)
	}
	d.log.Info("extraction batch processed", "batch_id", batchID, "accepted", out.Accepted, "rejected", out.Rejected, "returned", out.Returned, "rate", out.Rate)
	return out, nil
}

// decodeResult returns the valid per-comment results of one batch request,
// or the reason none are usable. Comments the model skipped, answered
// twice or answered invalidly are left out and go back to pending.
func decodeResult(cur anthropic.MessageBatchIndividualResponse, ids []int64) (map[int64]Result, string) {
	switch cur.Result.Type {
	case "succeeded":
	case "errored":
		return nil, "batch request errored"
	default:
		return nil, "batch request " + cur.Result.Type
	}
	msg := cur.Result.Message
	if msg.StopReason != anthropic.StopReasonEndTurn {
		return nil, "extractor stopped with " + string(msg.StopReason)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	var o output
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(text.String())))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, "extractor answer does not match the output contract"
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	results := map[int64]Result{}
	dupes := map[int64]bool{}
	for _, r := range o.Results {
		if !want[r.CommentID] {
			continue
		}
		if _, seen := results[r.CommentID]; seen {
			dupes[r.CommentID] = true
			continue
		}
		if r.validate() != nil {
			continue
		}
		results[r.CommentID] = r
	}
	for id := range dupes {
		delete(results, id)
	}
	return results, "extractor answer omitted or invalidated this comment"
}

func (d *Driver) apply(ctx context.Context, r Result, model, version string) error {
	if !r.IsReusable {
		if err := d.store.FinishComment(ctx, queries.FinishCommentParams{ID: r.CommentID, ExtractStatus: "rejected"}); err != nil {
			return fmt.Errorf("finish comment: %w", err)
		}
		return nil
	}
	err := d.store.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		if err := q.UpsertExtraction(ctx, queries.UpsertExtractionParams{
			CommentID: r.CommentID, Title: strings.TrimSpace(r.Title), Description: strings.TrimSpace(r.Description),
			PreferredApproach: strPtr(strings.TrimSpace(r.PreferredApproach)), AppliesToHint: strPtr(strings.TrimSpace(r.AppliesToHint)),
			Category: strings.TrimSpace(r.Category), LanguageAgnostic: r.LanguageAgnostic, Confidence: float32(r.Confidence),
			ExtractorModel: model, ExtractorVersion: version,
		}); err != nil {
			return fmt.Errorf("upsert extraction: %w", err)
		}
		return q.FinishComment(ctx, queries.FinishCommentParams{ID: r.CommentID, ExtractStatus: "accepted"})
	})
	if err != nil {
		return fmt.Errorf("store extraction: %w", err)
	}
	return nil
}

func (d *Driver) cache(ctx context.Context, hash, model, version string, r Result) error {
	// The stored hash was computed for the pinned model, so only results
	// from that model may be cached under it.
	if hash == "" || model != d.cfg.Model {
		return nil
	}
	r.CommentID = 0 // the cache is keyed by content, not by comment
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if err := d.store.PutCachedExtraction(ctx, queries.PutCachedExtractionParams{
		InputHash: hash, ExtractorVersion: version, Model: model, Result: data,
	}); err != nil {
		return fmt.Errorf("cache extraction: %w", err)
	}
	return nil
}

func (d *Driver) returnToPending(ctx context.Context, ids []int64, reason string) error {
	if err := d.store.ReturnCommentsToPending(ctx, queries.ReturnCommentsToPendingParams{
		MaxAttempts: int32(d.cfg.MaxAttempts), Reason: reason, Ids: ids,
	}); err != nil {
		return fmt.Errorf("return comments to pending: %w", err)
	}
	return nil
}

// customID encodes a request's comment ids: "c-<id>-<id>-<id>", with
// revert seeds' negative ids written "n<abs>". Three 19-digit ids fit the
// API's 64-character [A-Za-z0-9_-] limit.
func customID(cs []Comment) string {
	parts := make([]string, 0, len(cs)+1)
	parts = append(parts, "c")
	for _, c := range cs {
		if c.ID < 0 {
			parts = append(parts, "n"+strconv.FormatInt(-c.ID, 10))
		} else {
			parts = append(parts, strconv.FormatInt(c.ID, 10))
		}
	}
	return strings.Join(parts, "-")
}

func parseCustomID(s string) ([]int64, error) {
	parts := strings.Split(s, "-")
	if len(parts) < 2 || len(parts) > CommentsPerRequest+1 || parts[0] != "c" {
		return nil, fmt.Errorf("%w %q", errBadCustomID, s)
	}
	ids := make([]int64, 0, len(parts)-1)
	for _, p := range parts[1:] {
		neg := strings.HasPrefix(p, "n")
		id, err := strconv.ParseInt(strings.TrimPrefix(p, "n"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w %q", errBadCustomID, s)
		}
		if neg {
			id = -id
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func i32(n int) *int32 {
	v := int32(n)
	return &v
}
