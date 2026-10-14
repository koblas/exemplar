package exports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Branch is the one export branch per repo.
const Branch = "exemplar/exports"

// Writer writes one repo's files (gh.RepoWriter, with the exports app).
type Writer interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	GetFile(ctx context.Context, ref, path string) ([]byte, bool, error)
	ResetBranch(ctx context.Context, branch, sha string) error
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenPRFor(ctx context.Context, branch string) (gh.ConfigItem, string, bool, error)
	OpenPR(ctx context.Context, branch, base, title, body string) (gh.ConfigItem, string, error)
	EnableAutoMerge(ctx context.Context, nodeID string) error
}

// Store records what was proposed (store.Store).
type Store interface {
	GetExport(ctx context.Context, arg queries.GetExportParams) ([]queries.Export, error)
	UpsertExport(ctx context.Context, arg queries.UpsertExportParams) error
	SetExportState(ctx context.Context, arg queries.SetExportStateParams) error
	MarkExportCurrent(ctx context.Context, arg queries.MarkExportCurrentParams) error
	RecordExportRun(ctx context.Context, configSha string) error
}

// Outcome is one repo's export.
type Outcome struct {
	Repo    string   `json:"repo"`
	Files   int      `json:"files"`
	Changed []string `json:"changed,omitempty"`
	Edited  []string `json:"edited,omitempty"` // a team edited inside the markers: not updated
	Action  string   `json:"action"`           // pr | updated | pending | current | dry_run | skipped
	URL     string   `json:"url,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// Runner exports rules.
type Runner struct {
	Store Store
	// Writer returns a repo's writer; nil makes Run a dry run.
	Writer func(ctx context.Context, repo string) (Writer, error)
	Log    *slog.Logger
}

// Run exports every repo that configures exports.targets. One repo's
// failure does not stop the rest.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot) ([]Outcome, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	repos := make([]string, 0, len(snap.Repos))
	for name, rc := range snap.Repos {
		if rc.Exports != nil && len(rc.Exports.Targets) > 0 && rc.Enabled {
			repos = append(repos, name)
		}
	}
	slices.Sort(repos)
	// Drift counts config versions as the export job sees them.
	if r.Writer != nil && len(repos) > 0 {
		if err := r.Store.RecordExportRun(ctx, snap.Version); err != nil {
			return nil, fmt.Errorf("exports: record run: %w", err)
		}
	}
	var out []Outcome
	var errs []error
	for _, repo := range repos {
		o, err := r.repo(ctx, snap, repo)
		if err != nil {
			errs = append(errs, fmt.Errorf("exports %s: %w", repo, err))
			o = Outcome{Repo: repo, Action: "skipped", Reason: err.Error()}
		}
		out = append(out, o)
	}
	return out, errors.Join(errs...)
}

func (r Runner) repo(ctx context.Context, snap *config.Snapshot, repo string) (Outcome, error) {
	rc := snap.Repos[repo]
	if err := snap.CheckEligible(repo); err != nil {
		return Outcome{Repo: repo, Action: "skipped", Reason: err.Error()}, nil
	}
	eff, err := snap.Effective(repo, nil)
	if err != nil {
		return Outcome{}, err
	}
	files := Render(Exportable(eff.Rules), rc.Exports.Targets)
	o := Outcome{Repo: repo, Files: len(files)}
	if r.Writer == nil {
		o.Action = "dry_run"
		return o, nil
	}
	w, err := r.Writer(ctx, repo)
	if err != nil {
		return o, err
	}
	base, sha, err := w.DefaultBranch(ctx)
	if err != nil {
		return o, err
	}
	type change struct {
		f        File
		content  []byte
		base     string // the block on the default branch
		proposed bool   // this exact block is already in the open PR
	}
	var changes []change
	for _, f := range files {
		cur, exists, err := w.GetFile(ctx, base, f.Path)
		if err != nil {
			return o, err
		}
		curBlock := Current(string(cur))
		prev, err := r.Store.GetExport(ctx, queries.GetExportParams{Repo: repo, Path: f.Path})
		if err != nil {
			return o, err
		}
		// A block that is neither the one proposed nor the one it replaced
		// was edited by hand: leave the file until that is resolved.
		if len(prev) == 1 && curBlock != "" && Hash(curBlock) != prev[0].ContentHash && Hash(curBlock) != prev[0].BaseHash {
			o.Edited = append(o.Edited, f.Path)
			if err := r.Store.SetExportState(ctx, queries.SetExportStateParams{Repo: repo, Path: f.Path, State: "edited"}); err != nil {
				return o, err
			}
			continue
		}
		if curBlock == f.Block {
			// The default branch matches this config's rendering.
			if len(prev) == 1 {
				if err := r.Store.MarkExportCurrent(ctx, queries.MarkExportCurrentParams{Repo: repo, Path: f.Path, CurrentSha: snap.Version}); err != nil {
					return o, err
				}
			}
			continue
		}
		proposed := len(prev) == 1 && prev[0].State == "proposed" && prev[0].ContentHash == Hash(f.Block)
		changes = append(changes, change{f: f, content: []byte(Merge(string(cur), exists, f)), base: curBlock, proposed: proposed})
	}
	if len(changes) == 0 {
		o.Action = "current"
		return o, nil
	}
	pr, node, found, err := w.OpenPRFor(ctx, Branch)
	if err != nil {
		return o, err
	}
	// The open PR already carries this rendering: leave it alone rather
	// than force-push the same content every run.
	if found && !slices.ContainsFunc(changes, func(c change) bool { return !c.proposed }) {
		o.Action, o.URL = "pending", pr.URL
		return o, nil
	}
	// One branch and PR per repo, rebuilt from the default branch each
	// time, so it always holds exactly the current rendering.
	if err := w.ResetBranch(ctx, Branch, sha); err != nil {
		return o, err
	}
	for _, c := range changes {
		if err := w.PutFile(ctx, Branch, c.f.Path, c.content, "Update Exemplar rules in "+c.f.Path); err != nil {
			return o, err
		}
		o.Changed = append(o.Changed, c.f.Path)
	}
	o.Action = "updated"
	if !found {
		if pr, node, err = w.OpenPR(ctx, Branch, base, "Update coding-agent instructions from Exemplar rules", prBody(snap, o.Changed, o.Edited)); err != nil {
			return o, err
		}
		o.Action = "pr"
	}
	o.URL = pr.URL
	// Only the generated blocks (or files Exemplar creates whole) change,
	// so the PR may auto-merge where the repo allows it; branch protection
	// still applies (spec §10.2).
	auto := rc.Exports.AutoMergeGenerated
	if auto {
		if err := w.EnableAutoMerge(ctx, node); err != nil {
			r.Log.Warn("export PR auto-merge not enabled", "repo", repo, "err", err)
			auto = false
		}
	}
	for _, c := range changes {
		if err := r.Store.UpsertExport(ctx, queries.UpsertExportParams{Repo: repo, Path: c.f.Path, Target: c.f.Target,
			ContentHash: Hash(c.f.Block), BaseHash: hashOrEmpty(c.base), ConfigSha: snap.Version, PrUrl: &o.URL, State: "proposed", AutoMerged: auto}); err != nil {
			return o, err
		}
	}
	return o, nil
}

func hashOrEmpty(block string) string {
	if block == "" {
		return ""
	}
	return Hash(block)
}

func prBody(snap *config.Snapshot, changed, edited []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exemplar renders each active rule's one-line `export.summary` into your coding-agent instruction files (spec §10.2), from exemplar-config `%s`.\n\n", snap.Version)
	b.WriteString("Only the text between the `BEGIN exemplar` and `END exemplar` markers is generated; everything outside them is yours and is never changed. To change what agents are told, change the rules in exemplar-config.\n\nFiles:\n\n")
	for _, p := range changed {
		b.WriteString("- `" + p + "`\n")
	}
	if len(edited) > 0 {
		b.WriteString("\nNot updated, because the generated block was edited by hand (move the text outside the markers, or restore them):\n\n")
		for _, p := range edited {
			b.WriteString("- `" + p + "`\n")
		}
	}
	return b.String()
}
