package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Feedback events recorded by code, with no human action in
// Exemplar's UI: a finding withheld because a reviewer already raised it
// (human_agreed), and a posted suggestion the author committed
// (applied_suggestion).

// Event kinds, roles and sources (feedback_events).
const (
	EventHumanAgreed       = "human_agreed"
	EventAppliedSuggestion = "applied_suggestion"

	RoleAuthor   = "author"
	RoleReviewer = "reviewer"
	RoleOwner    = "owner"
	RoleObserver = "observer"

	ViaInferred     = "inferred"
	ViaSlash        = "slash"
	ViaCodeScanning = "code_scanning"
	ViaWebhook      = "webhook"
	ViaReaction     = "reaction"
	ViaLink         = "link"
)

// defaultRoleWeights are the default role weights: author 1, reviewer 2, owner
// 4, and observers count for nothing.
var defaultRoleWeights = map[string]float64{RoleAuthor: 1, RoleReviewer: 2, RoleOwner: 4, RoleObserver: 0}

// DefaultReactionWeight is a reaction's weight when
// feedback.reaction_weight is unset.
const DefaultReactionWeight = 0.5

// ReactionWeight is feedback.reaction_weight, or its default.
func ReactionWeight(cfg *schema.FeedbackConfig) float32 {
	if cfg != nil && cfg.ReactionWeight > 0 {
		return float32(cfg.ReactionWeight)
	}
	return DefaultReactionWeight
}

// RoleWeight is the weight of role's signals: feedback.role_weights when
// configured, else the spec's defaults.
func RoleWeight(cfg *schema.FeedbackConfig, role string) float32 {
	if cfg != nil {
		if w, ok := cfg.RoleWeights[role]; ok {
			return float32(w)
		}
	}
	return float32(defaultRoleWeights[role])
}

// Event is one feedback event to record.
type Event struct {
	FindingID int64
	Kind      string
	Actor     string
	Role      string
	Via       string
	// Weight overrides the role's weight (reactions).
	Weight *float32
	// ReasonText is the free text a person gave with the signal.
	ReasonText string
	Evidence   map[string]any
	// Thread marks a signal on the finding's comment thread (a reply,
	// a resolution, a slash command, a feedback link): on a class-of-bug
	// group's comment it applies to every open member (FR-C9).
	Thread bool
}

// RecordEvents writes events, weighted by role. An event already recorded
// for the same (finding, kind, actor) is skipped. It returns how many were
// new; one failure does not stop the rest.
func RecordEvents(ctx context.Context, st *store.Store, cfg *schema.FeedbackConfig, evs []Event) (int, error) {
	if st == nil {
		return 0, nil
	}
	n := 0
	var errs []error
	evs, err := expandGroups(ctx, st, evs)
	if err != nil {
		errs = append(errs, err)
	}
	for _, ev := range evs {
		var evidence []byte
		if ev.Evidence != nil {
			var err error
			if evidence, err = json.Marshal(ev.Evidence); err != nil {
				errs = append(errs, fmt.Errorf("feedback event %s on finding %d: evidence: %w", ev.Kind, ev.FindingID, err))
				continue
			}
		}
		weight := RoleWeight(cfg, ev.Role)
		if ev.Weight != nil {
			weight = *ev.Weight
		}
		rows, err := st.InsertFeedbackEvent(ctx, queries.InsertFeedbackEventParams{
			FindingID: ev.FindingID, Kind: ev.Kind, Actor: ev.Actor, Role: ev.Role,
			Weight: weight, Via: ev.Via, Evidence: evidence, ReasonText: nonEmpty(ev.ReasonText),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("feedback event %s on finding %d: %w", ev.Kind, ev.FindingID, err))
			continue
		}
		n += int(rows)
		if rows > 0 {
			if err := routeLabel(ctx, st, ev); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return n, errors.Join(errs...)
}

// routeLabel feeds people's judgments to verifier calibration: a wrong
// is a "wrong" label, an author's
// as an author signal and anyone else's as a human review; an owner's
// adjudication is a human review either way, and replaces author signals.
func routeLabel(ctx context.Context, st *store.Store, ev Event) error {
	var label string
	source := "human_review"
	switch ev.Kind {
	case "fb_wrong":
		label = "wrong"
		if ev.Role == RoleAuthor {
			source = "author_signal"
		}
	case "adjudicated_overturn":
		label = "wrong"
	case "adjudicated_uphold":
		label = "true"
	default:
		return nil
	}
	if ev.Role == RoleObserver {
		return nil
	}
	if err := st.UpsertVerifierLabel(ctx, queries.UpsertVerifierLabelParams{FindingID: ev.FindingID, Label: label, Source: source}); err != nil {
		return fmt.Errorf("verifier label for finding %d: %w", ev.FindingID, err)
	}
	return nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// humanAgreedEvents are the human_agreed events of a persisted review's
// human duplicates: the reviewer whose comment raised the
// finding first agreed with it.
func humanAgreedEvents(res *engine.Result) []Event {
	var out []Event
	for _, f := range res.Findings {
		if f.ID == 0 || f.Suppressed != engine.SuppressHumanDuplicate || f.HumanMatch == nil {
			continue
		}
		out = append(out, Event{
			FindingID: f.ID, Kind: EventHumanAgreed, Actor: f.HumanMatch.Author, Role: RoleReviewer, Via: ViaInferred,
			Evidence: map[string]any{"comment_id": f.HumanMatch.ID},
		})
	}
	return out
}

// minSuggestionChars is the shortest suggestion, by its longest line's
// non-space characters, that may be found away from its anchor: a short
// line such as "}" matches too much code to count.
const minSuggestionChars = 8

// suggestionWindow is how far from its anchor a committed suggestion may
// have moved, when other edits shifted the lines above it.
const suggestionWindow = 50

// AppliedSuggestions returns the open findings, posted with a valid
// suggestion on an earlier head, whose suggested text is now in the file
// at the anchor (suggestion acceptance tracking): exactly at the anchored
// start line, or, for a non-trivial suggestion, exactly once within 50
// lines of it.
func AppliedSuggestions(ctx context.Context, p *engine.Prepared, open []OpenFinding) []OpenFinding {
	var out []OpenFinding
	files := map[string][]string{}
	for _, o := range open {
		if o.HeadSha == p.PR.HeadSHA || o.SuggestedFix == nil || o.FixValid == nil || !*o.FixValid || o.Side != "RIGHT" {
			continue
		}
		lines, ok := files[o.Path]
		if !ok {
			data, err := p.Workspace.Show(ctx, p.PR.HeadSHA, o.Path)
			if err == nil {
				lines = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			}
			files[o.Path] = lines
		}
		fix := strings.Split(engine.SuggestionText(engine.Finding{Fix: *o.SuggestedFix}), "\n")
		if suggestionAt(lines, fix, int(o.StartLine)) {
			out = append(out, o)
		}
	}
	return out
}

func suggestionAt(lines, fix []string, start int) bool {
	match := func(i int) bool { // i is 0-based
		if i < 0 || i+len(fix) > len(lines) {
			return false
		}
		for j, l := range fix {
			if lines[i+j] != l {
				return false
			}
		}
		return true
	}
	if match(start - 1) {
		return true
	}
	longest := 0
	for _, l := range fix {
		longest = max(longest, len(strings.TrimSpace(l)))
	}
	if longest < minSuggestionChars {
		return false
	}
	found := 0
	for i := max(0, start-1-suggestionWindow); i <= start-1+suggestionWindow; i++ {
		if match(i) {
			found++
		}
	}
	return found == 1
}

// RecordAppliedSuggestions records applied_suggestion events for applied,
// attributed to the PR's author: GitHub's "Commit suggestion" commits as
// whoever clicked it, normally the author.
func RecordAppliedSuggestions(ctx context.Context, st *store.Store, p *engine.Prepared, applied []OpenFinding) (int, error) {
	evs := make([]Event, 0, len(applied))
	for _, o := range applied {
		evs = append(evs, Event{
			FindingID: o.ID, Kind: EventAppliedSuggestion, Actor: p.PR.Author, Role: RoleAuthor, Via: ViaInferred,
			Evidence: map[string]any{"head_sha": p.PR.HeadSHA, "posted_on": o.HeadSha},
		})
	}
	return RecordEvents(ctx, st, p.Effective.Global.Feedback, evs)
}

// expandGroups repeats each thread event for every open member of the
// finding's class-of-bug group; other events pass through. A lookup that
// fails keeps the event on its own finding.
func expandGroups(ctx context.Context, st *store.Store, evs []Event) ([]Event, error) {
	out := make([]Event, 0, len(evs))
	var errs []error
	for _, ev := range evs {
		if !ev.Thread {
			out = append(out, ev)
			continue
		}
		ids, err := st.GroupMembers(ctx, ev.FindingID)
		if err != nil {
			errs = append(errs, fmt.Errorf("feedback event %s on finding %d: group: %w", ev.Kind, ev.FindingID, err))
		}
		if err != nil || len(ids) == 0 {
			out = append(out, ev)
			continue
		}
		for _, id := range ids {
			m := ev
			m.FindingID = id
			out = append(out, m)
		}
	}
	return out, errors.Join(errs...)
}
