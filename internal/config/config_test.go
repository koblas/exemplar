package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/schema"
)

const exampleConfig = "../../examples/exemplar-config"

func loadExample(t *testing.T) *Snapshot {
	t.Helper()
	s, err := LoadSnapshot(os.DirFS(exampleConfig), "test-version")
	if err != nil {
		t.Fatalf("LoadSnapshot(example): %v", err)
	}
	return s
}

func TestLoadExampleConfig(t *testing.T) {
	s := loadExample(t)
	if len(s.Rules) < 3 {
		t.Fatalf("got %d rules, want the 3 example rules", len(s.Rules))
	}
	if _, ok := s.Repos["exemplar-sandbox/playground"]; !ok {
		t.Fatalf("sandbox repo config missing: %v", s.Repos)
	}
	for _, r := range s.Rules {
		if len(r.VersionSHA) != 40 || r.Scope != "global" {
			t.Errorf("rule %s: VersionSHA=%q Scope=%q", r.ID, r.VersionSHA, r.Scope)
		}
	}
}

func TestBlobSHAMatchesGit(t *testing.T) {
	data := []byte("hello\n")
	// git hash-object of "hello\n".
	if got, want := BlobSHA(data), "ce013625030ba8dba906f756967f9e9ca394464a"; got != want {
		t.Fatalf("BlobSHA = %s, want %s", got, want)
	}
}

func baseTree(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	err := filepathWalk(exampleConfig, func(rel string, data []byte) {
		fsys[rel] = &fstest.MapFile{Data: data}
	})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

func filepathWalk(root string, fn func(rel string, data []byte)) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fn(filepath.ToSlash(rel), data)
		return nil
	})
}

func TestLoadSnapshotReportsAllProblems(t *testing.T) {
	fsys := baseTree(t)
	delete(fsys, RetentionPath)
	// Wrong directory for the category, and an id that doesn't match the file.
	fsys["rules/style/propagate-context.yaml"] = fsys["rules/reliability/propagate-context.yaml"]
	fsys["rules/reliability/renamed.yaml"] = fsys["rules/maintainability/wrap-errors-with-cause.yaml"]
	fsys["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\ndisabled_global_rules: [nope]\n")}
	fsys["evals/ghost-rule/cases.yaml"] = &fstest.MapFile{Data: []byte("rule: ghost-rule\ncases:\n  - {name: a, ref: a/b@abcdef1, expect: []}\n")}

	_, err := LoadSnapshot(fsys, "v")
	var probs Problems
	if !errors.As(err, &probs) {
		t.Fatalf("LoadSnapshot = %v, want Problems", err)
	}
	want := []string{
		RetentionPath,
		"rules/style/propagate-context.yaml",
		"rules/reliability/renamed.yaml",
		"repos/exemplar-sandbox/playground.yaml",
		"evals/ghost-rule/cases.yaml",
	}
	for _, p := range want {
		found := false
		for _, pr := range probs {
			if pr.Path == p {
				found = true
			}
		}
		if !found {
			t.Errorf("no problem reported for %s; got:\n%v", p, probs)
		}
	}
	if !strings.Contains(probs.Error(), "already defined") {
		t.Errorf("duplicate rule id not reported:\n%v", probs)
	}
}

func TestEffective(t *testing.T) {
	s := loadExample(t)
	const repo = "exemplar-sandbox/playground"

	e, err := s.Effective(repo, nil)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if e.ConfigVersion != "test-version" || len(e.Rules) != len(s.Rules) {
		t.Fatalf("Effective = version %q, %d rules", e.ConfigVersion, len(e.Rules))
	}

	rc := s.Repos[repo]
	rc.DisabledGlobalRules = []string{"wrap-errors-with-cause"}
	s.Repos[repo] = rc
	e, err = s.Effective(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range e.Rules {
		if r.ID == "wrap-errors-with-cause" {
			t.Error("disabled global rule still in effective rules")
		}
	}

	repoRule := Rule{Scope: repo}
	repoRule.ID, repoRule.Status = "propagate-context", "active"
	if _, err := s.Effective(repo, []Rule{repoRule}); err == nil {
		t.Error("repo rule shadowing a global rule id was accepted")
	}
	killed := Rule{Scope: repo}
	killed.ID, killed.Status = "local-killed", "killed"
	e, _ = s.Effective(repo, []Rule{killed})
	for _, r := range e.Rules {
		if r.ID == "local-killed" {
			t.Error("killed repo rule is runnable")
		}
	}
}

func TestCheckEligible(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Snapshot)
		repo   string
		want   error
	}{
		{"opted in", func(*Snapshot) {}, "exemplar-sandbox/playground", nil},
		{"not opted in", func(*Snapshot) {}, "acme/other", ErrNotOptedIn},
		{"kill switch", func(s *Snapshot) {
			rc := s.Repos["exemplar-sandbox/playground"]
			rc.Enabled = false
			s.Repos["exemplar-sandbox/playground"] = rc
		}, "exemplar-sandbox/playground", ErrRepoDisabled},
		{"global off", func(s *Snapshot) { s.Global.Enabled = false }, "exemplar-sandbox/playground", ErrDisabledGlobally},
		{"denylisted wins over opt-in", func(s *Snapshot) {
			s.Eligibility.Denylist = append(s.Eligibility.Denylist, schema.DenylistEntry{Repo: "Exemplar-Sandbox/Playground", Reason: "test"})
		}, "exemplar-sandbox/playground", ErrDenylisted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := loadExample(t)
			tt.modify(s)
			err := s.CheckEligible(tt.repo)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("CheckEligible = %v, want %v", err, tt.want)
			}
			if _, err := s.Effective(tt.repo, nil); (err == nil) != (tt.want == nil) {
				t.Fatalf("Effective error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestGitFSReadsBaseNotHead is the system-rule-1 guarantee: a PR head that
// weakens .exemplar/ config has no effect on its own review.
func TestGitFSReadsBaseNotHead(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q", "-b", "main")
	rule, err := os.ReadFile(filepath.Join(exampleConfig, "rules/reliability/propagate-context.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	write(".exemplar/rules/propagate-context.yaml", string(rule))
	write("main.go", "package main\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")

	write(".exemplar/rules/propagate-context.yaml", strings.Replace(string(rule), "status: active", "status: killed", 1))
	git("commit", "-q", "-am", "head weakens its own rule")

	fsys, err := GitFS(context.Background(), dir, base, ".exemplar")
	if err != nil {
		t.Fatalf("GitFS: %v", err)
	}
	if _, ok := fsys["main.go"]; ok {
		t.Error("GitFS returned a file outside the requested prefix")
	}
	rules, err := LoadRepoRules(fsys, "acme/app")
	if err != nil {
		t.Fatalf("LoadRepoRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Status != "active" || rules[0].Scope != "acme/app" {
		t.Fatalf("rules from base = %+v, want the active base rule", rules)
	}

	if _, err := GitFS(context.Background(), dir, "--output=/tmp/x", ".exemplar"); err == nil {
		t.Error("GitFS accepted an option-like revision")
	}
}

func TestMergeSafetyConfigChecks(t *testing.T) {
	ok := baseTree(t)
	ok[GlobalPath] = &fstest.MapFile{Data: append(slices.Clone(ok[GlobalPath].Data), []byte("merge_safety:\n  weights: { churn_30d: 0 }\n")...)}
	if _, err := LoadSnapshot(ok, "v"); err != nil {
		t.Fatalf("valid merge_safety rejected: %v", err)
	}

	bad := baseTree(t)
	bad[GlobalPath] = &fstest.MapFile{Data: append(slices.Clone(bad[GlobalPath].Data), []byte("merge_safety:\n  weights: { incident_90d: 0.3 }\n")...)}
	bad["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nsensitive_paths: [\"db/[migrations\"]\n")}
	_, err := LoadSnapshot(bad, "v")
	var probs Problems
	if !errors.As(err, &probs) {
		t.Fatalf("LoadSnapshot = %v, want Problems", err)
	}
	for _, p := range []string{GlobalPath, "repos/exemplar-sandbox/playground.yaml"} {
		if !slices.ContainsFunc(probs, func(pr Problem) bool { return pr.Path == p }) {
			t.Errorf("no problem reported for %s; got:\n%v", p, probs)
		}
	}
}

func TestStage2BarsCannotBeLowered(t *testing.T) {
	ok := baseTree(t)
	ok["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nauto_approve: { enabled: false, min_merge_safety_score: 0.9, min_review_confidence: 0.95 }\n")}
	if _, err := LoadSnapshot(ok, "v"); err != nil {
		t.Fatalf("raised bars rejected: %v", err)
	}
	bad := baseTree(t)
	bad["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nauto_approve: { enabled: false, min_merge_safety_score: 0.5, min_review_confidence: 0.6 }\n")}
	_, err := LoadSnapshot(bad, "v")
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 2 {
		t.Fatalf("lowered bars = %v", err)
	}
	for _, want := range []string{"min_merge_safety_score 0.5 is below the 0.7 floor", "min_review_confidence 0.6 is below the 0.8 floor"} {
		if !slices.ContainsFunc(probs, func(p Problem) bool { return strings.Contains(p.Err.Error(), want) }) {
			t.Errorf("no %q in %v", want, probs)
		}
	}
}

func TestAutoApproveConfigChecks(t *testing.T) {
	ok := baseTree(t)
	ok["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nauto_approve:\n  enabled: true\n  paths: [\"docs/**\"]\n" +
		"  bot_authors: [{identity: \"renovate[bot]\", paths: [\"docs/deps/**\"]}]\n  title_pattern: \"^docs: \"\n  max_files: 5\n  max_changed_lines: 200\n")}
	if _, err := LoadSnapshot(ok, "v"); err != nil {
		t.Fatalf("valid auto_approve rejected: %v", err)
	}
	bad := baseTree(t)
	bad["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nauto_approve:\n  enabled: true\n  paths: [\"docs/[x\"]\n" +
		"  extra_denylist: [\"a/[\"]\n  bot_authors: [{identity: \"renovate[bot]\", paths: [\"[\"]}]\n  title_pattern: \"(\"\n  max_files: 11\n  max_changed_lines: 5000\n")}
	_, err := LoadSnapshot(bad, "v")
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 6 {
		t.Fatalf("bad auto_approve = %v", err)
	}
}

func TestBugLaneClassesAllowlisted(t *testing.T) {
	bad := baseTree(t)
	bad[GlobalPath] = &fstest.MapFile{Data: []byte(strings.Replace(string(bad[GlobalPath].Data), "bug.off-by-one:", "bug.made-up:", 1))}
	_, err := LoadSnapshot(bad, "v")
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 1 || !strings.Contains(probs[0].Err.Error(), "bug.made-up is not an allowlisted bug class") {
		t.Fatalf("unknown class = %v", err)
	}
}

// A repo may lower the round cap, never raise it (FR-C8).
func TestRoundsCap(t *testing.T) {
	ok := baseTree(t)
	ok["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nrounds: { max: 2 }\n")}
	snap, err := LoadSnapshot(ok, "v")
	if err != nil {
		t.Fatalf("lowered cap rejected: %v", err)
	}
	eff, err := snap.Effective("exemplar-sandbox/playground", nil)
	if err != nil || eff.RoundsMax() != 2 || snap.Global.RoundsMax() != 3 {
		t.Fatalf("rounds = %v, global %d, %v", eff, snap.Global.RoundsMax(), err)
	}
	bad := baseTree(t)
	bad["repos/exemplar-sandbox/playground.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\nrounds: { max: 9 }\n")}
	if _, err := LoadSnapshot(bad, "v"); err == nil || !strings.Contains(err.Error(), "rounds.max 9 is above global.yaml's 3") {
		t.Errorf("raised cap = %v", err)
	}
}

// posting.class_threshold defaults to 3 and rejects zero (FR-C9).
func TestClassThreshold(t *testing.T) {
	snap, err := LoadSnapshot(baseTree(t), "v")
	if err != nil || snap.Global.ClassThreshold() != 3 {
		t.Fatalf("example threshold = %v, %v", snap, err)
	}
	set := baseTree(t)
	set[GlobalPath] = &fstest.MapFile{Data: []byte(strings.Replace(string(set[GlobalPath].Data), "class_threshold: 3", "class_threshold: 5", 1))}
	if snap, err := LoadSnapshot(set, "v"); err != nil || snap.Global.ClassThreshold() != 5 {
		t.Errorf("explicit threshold = %v", err)
	}
	if (schema.GlobalConfig{}).ClassThreshold() != schema.DefaultClassThreshold {
		t.Error("absent posting is not the default")
	}
	bad := baseTree(t)
	bad[GlobalPath] = &fstest.MapFile{Data: []byte(strings.Replace(string(bad[GlobalPath].Data), "class_threshold: 3", "class_threshold: 0", 1))}
	if _, err := LoadSnapshot(bad, "v"); err == nil {
		t.Error("class_threshold 0 accepted")
	}
}
