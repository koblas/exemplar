// Package bench is the monorepo benchmark: shallow sparse clone time, zoekt
// full and incremental index time and size, query latency, and ctags symbol
// coverage per language. Its numbers size exemplar-search and confirm the v1
// language list.
//
// It drives the standard binaries (git, zoekt-git-index, zoekt-webserver,
// universal-ctags) so it measures what exemplar-search will run. Query
// latency is measured against zoekt-webserver's JSON API, so process
// start-up is excluded.
package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config configures a benchmark run.
type Config struct {
	// RepoURL is the monorepo clone URL (or a local path).
	RepoURL string
	// Branch is the default branch to index (default "main").
	Branch string
	// WorkDir holds the clones and index; it should be on the same kind of
	// SSD as an exemplar-search node.
	WorkDir string
	// SparseDirs are the directories a typical PR touches, for the worker
	// checkout measurement. Empty measures a root-only cone.
	SparseDirs []string
	// IncrementalDepths are how many commits back to start each
	// incremental-index measurement from (default 1, 10, 100).
	IncrementalDepths []int
	// Queries are run against the index; defaults cover literal, regex,
	// symbol and import-pattern searches.
	Queries []string
	// QueryRepeats is how many times each query runs (default 20).
	QueryRepeats int
	// CtagsSample is the maximum files sampled per language (default 300).
	CtagsSample int
	// Token, if set, is sent to git as an HTTP header via environment config.
	Token string
}

// Report is a benchmark run's result.
type Report struct {
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	StartedAt time.Time `json:"started_at"`

	Repository struct {
		Files int   `json:"files"`
		Bytes int64 `json:"checked_out_bytes"`
	} `json:"repository"`

	SparseClone struct {
		ColdSeconds float64 `json:"cold_seconds"`
		WarmSeconds float64 `json:"warm_seconds"`
		Steps       []Step  `json:"cold_steps"`
	} `json:"sparse_clone"`

	Mirror struct {
		Seconds float64 `json:"seconds"`
		Bytes   int64   `json:"bytes"`
	} `json:"mirror"`

	FullIndex struct {
		Seconds    float64 `json:"seconds"`
		IndexBytes int64   `json:"index_bytes"`
		Shards     int     `json:"shards"`
	} `json:"full_index"`

	Incremental []Incremental `json:"incremental_index"`

	Queries []QueryStats `json:"queries"`

	Ctags []LanguageCoverage `json:"ctags_coverage"`

	// Sizing is the derived exemplar-search recommendation.
	Sizing Sizing `json:"sizing"`

	Notes []string `json:"notes,omitempty"`
}

// Step is one timed command.
type Step struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
}

// Incremental is one incremental re-index measurement.
type Incremental struct {
	CommitsBack  int     `json:"commits_back"`
	FilesChanged int     `json:"files_changed"`
	Seconds      float64 `json:"seconds"`
}

// QueryStats is latency for one query.
type QueryStats struct {
	Query   string  `json:"query"`
	Matches int     `json:"matches"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
}

// LanguageCoverage is ctags symbol coverage for one language.
type LanguageCoverage struct {
	Language       string  `json:"language"`
	FilesInRepo    int     `json:"files_in_repo"`
	Sampled        int     `json:"sampled"`
	WithSymbols    int     `json:"with_symbols"`
	Coverage       float64 `json:"coverage"`
	SymbolsPerFile float64 `json:"symbols_per_file"`
}

// Sizing derives exemplar-search disk and memory from the measurements.
type Sizing struct {
	// DiskBytesPerReplica covers mirror + index + one rebuild generation
	// beside the serving one, with 25% headroom.
	DiskBytesPerReplica int64 `json:"disk_bytes_per_replica"`
	// RAMBytesForHotShards keeps the whole index in page cache.
	RAMBytesForHotShards int64 `json:"ram_bytes_for_hot_shards"`
	// SourceMultiple is disk ÷ checked-out source size (spec estimate: 3–4×).
	SourceMultiple float64 `json:"source_multiple"`
}

// DefaultQueries cover the context tools' query shapes.
var DefaultQueries = []string{
	`context.Background`,             // literal, common
	`func \w+\(ctx context\.Context`, // regex
	`sym:Handler`,                    // symbol
	`"net/http"`,                     // Go import pattern
	`#include\s+[<"]`,                // C/C++ include pattern
	`from\s+['"].*/`,                 // TS/JS import pattern
	`lang:go TODO`,                   // language filter
	`file:_test\.go$ t\.Run`,         // path filter
}

// languages maps extensions to the v1 language list.
var languages = map[string]string{
	".go": "Go",
	".c":  "C/C++", ".h": "C/C++", ".cc": "C/C++", ".cpp": "C/C++", ".cxx": "C/C++", ".hpp": "C/C++", ".hh": "C/C++",
	".ts": "TypeScript/JavaScript", ".tsx": "TypeScript/JavaScript", ".js": "TypeScript/JavaScript", ".jsx": "TypeScript/JavaScript", ".mjs": "TypeScript/JavaScript",
	".py":   "Python",
	".java": "Java/Kotlin", ".kt": "Java/Kotlin", ".kts": "Java/Kotlin",
}

// LanguageOf returns the v1 language for path, or "".
func LanguageOf(path string) string { return languages[strings.ToLower(filepath.Ext(path))] }

// Tool and server errors.
var (
	errMissingTools = errors.New("bench: missing tools")
	errNotUniversal = errors.New("bench: ctags is not universal-ctags")
	errNotReady     = errors.New("zoekt-webserver did not become ready")
)

// RequiredTools are the binaries Run needs.
var RequiredTools = []string{"git", "zoekt-git-index", "zoekt-webserver", "ctags"}

// CheckTools reports missing binaries, and whether ctags is universal-ctags.
func CheckTools() error {
	var missing []string
	for _, t := range RequiredTools {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", errMissingTools, strings.Join(missing, ", "))
	}
	out, _ := exec.Command("ctags", "--version").Output() //nolint:noctx // one-off probe
	if !bytes.Contains(out, []byte("Universal Ctags")) {
		return errNotUniversal
	}
	return nil
}

// Run executes the benchmark.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := CheckTools(); err != nil {
		return nil, err
	}
	cfg.defaults()
	r := &Report{Repo: cfg.RepoURL, Branch: cfg.Branch, StartedAt: time.Now().UTC()}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("bench: work dir: %w", err)
	}
	env := gitEnv(cfg.Token)

	// 1. Worker checkout, cold then warm, with the worker's command sequence.
	cold, steps, err := sparseClone(ctx, env, cfg, filepath.Join(cfg.WorkDir, "wt-cold"))
	if err != nil {
		return nil, fmt.Errorf("sparse clone: %w", err)
	}
	r.SparseClone.ColdSeconds, r.SparseClone.Steps = cold, steps
	if warm, _, err := sparseClone(ctx, env, cfg, filepath.Join(cfg.WorkDir, "wt-warm")); err == nil {
		r.SparseClone.WarmSeconds = warm
	}

	// 2. Full-object bare mirror (exemplar-search's only git copy).
	mirror := filepath.Join(cfg.WorkDir, "mirror.git")
	_ = os.RemoveAll(mirror)
	secs, err := timed(ctx, env, "git", "clone", "--mirror", "-q", cfg.RepoURL, mirror)
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	r.Mirror.Seconds, r.Mirror.Bytes = secs, dirSize(mirror)
	files, bytes, err := treeStats(ctx, env, mirror, cfg.Branch)
	if err != nil {
		return nil, err
	}
	r.Repository.Files, r.Repository.Bytes = len(files), bytes

	// 3. Full index with ctags symbols.
	index := filepath.Join(cfg.WorkDir, "index")
	_ = os.RemoveAll(index)
	if r.FullIndex.Seconds, err = timed(ctx, env, "zoekt-git-index", "-index", index, "-branches", cfg.Branch, "-require_ctags", "-incremental=false", mirror); err != nil {
		return nil, fmt.Errorf("full index: %w", err)
	}
	r.FullIndex.IndexBytes = dirSize(index)
	r.FullIndex.Shards = countFiles(index, ".zoekt")

	// 4. Incremental index after realistic pushes.
	for _, back := range cfg.IncrementalDepths {
		inc, err := incremental(ctx, env, cfg, mirror, back)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("incremental %d commits back: %v", back, err))
			continue
		}
		r.Incremental = append(r.Incremental, *inc)
	}

	// 5. Query latency against zoekt-webserver.
	if r.Queries, err = queryLatency(ctx, index, cfg.Queries, cfg.QueryRepeats); err != nil {
		return nil, fmt.Errorf("queries: %w", err)
	}

	// 6. ctags coverage per language, from the warm checkout's files.
	r.Ctags = ctagsCoverage(ctx, env, mirror, cfg.Branch, files, cfg.CtagsSample, filepath.Join(cfg.WorkDir, "ctags"))

	r.Sizing = size(r)
	return r, nil
}

func (c *Config) defaults() {
	if c.Branch == "" {
		c.Branch = "main"
	}
	if len(c.IncrementalDepths) == 0 {
		c.IncrementalDepths = []int{1, 10, 100}
	}
	if len(c.Queries) == 0 {
		c.Queries = DefaultQueries
	}
	if c.QueryRepeats <= 0 {
		c.QueryRepeats = 20
	}
	if c.CtagsSample <= 0 {
		c.CtagsSample = 300
	}
}

func sparseClone(ctx context.Context, env []string, cfg Config, dir string) (float64, []Step, error) {
	_ = os.RemoveAll(dir)
	var steps []Step
	run := func(name string, args ...string) error {
		s, err := timed(ctx, env, "git", args...)
		steps = append(steps, Step{Name: name, Seconds: s})
		return err
	}
	if err := run("clone", "clone", "-q", "--filter=blob:none", "--depth=1", "--no-checkout", "--sparse", "--branch", cfg.Branch, cfg.RepoURL, dir); err != nil {
		return 0, nil, err
	}
	sparse := append([]string{"-C", dir, "sparse-checkout", "set", "--cone", "--"}, cfg.SparseDirs...)
	if err := run("sparse-checkout", sparse...); err != nil {
		return 0, nil, err
	}
	if err := run("checkout", "-C", dir, "checkout", "-q", cfg.Branch); err != nil {
		return 0, nil, err
	}
	var total float64
	for _, s := range steps {
		total += s.Seconds
	}
	return total, steps, nil
}

// incremental indexes the branch as it was `back` commits ago, then moves
// the branch to its tip and times the incremental re-index.
func incremental(ctx context.Context, env []string, cfg Config, mirror string, back int) (*Incremental, error) {
	old, err := gitOutput(ctx, env, mirror, "rev-parse", fmt.Sprintf("%s~%d", cfg.Branch, back))
	if err != nil {
		return nil, err
	}
	tip, err := gitOutput(ctx, env, mirror, "rev-parse", cfg.Branch)
	if err != nil {
		return nil, err
	}
	changed, err := gitOutput(ctx, env, mirror, "diff", "--name-only", old, tip)
	if err != nil {
		return nil, err
	}
	const benchRef = "refs/heads/exemplar-bench"
	index := filepath.Join(cfg.WorkDir, fmt.Sprintf("index-inc-%d", back))
	_ = os.RemoveAll(index)
	if _, err := gitOutput(ctx, env, mirror, "update-ref", benchRef, old); err != nil {
		return nil, err
	}
	defer func() { _, _ = gitOutput(context.WithoutCancel(ctx), env, mirror, "update-ref", "-d", benchRef) }()
	if _, err := timed(ctx, env, "zoekt-git-index", "-index", index, "-branches", "exemplar-bench", "-require_ctags", mirror); err != nil {
		return nil, err
	}
	if _, err := gitOutput(ctx, env, mirror, "update-ref", benchRef, tip); err != nil {
		return nil, err
	}
	secs, err := timed(ctx, env, "zoekt-git-index", "-index", index, "-branches", "exemplar-bench", "-require_ctags", "-incremental", mirror)
	if err != nil {
		return nil, err
	}
	n := 0
	for l := range strings.SplitSeq(changed, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return &Incremental{CommitsBack: back, FilesChanged: n, Seconds: secs}, nil
}

func queryLatency(ctx context.Context, index string, queries []string, repeats int) ([]QueryStats, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bench: listen: %w", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := exec.CommandContext(sctx, "zoekt-webserver", "-index", index, "-listen", addr, "-rpc")
	srv.Stdout, srv.Stderr = io.Discard, io.Discard
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("bench: start zoekt-webserver: %w", err)
	}
	defer func() { cancel(); _ = srv.Wait() }()

	client := &http.Client{Timeout: 30 * time.Second}
	search := func(q string) (int, time.Duration, error) {
		body, err := json.Marshal(map[string]any{"Q": q, "Opts": map[string]any{"MaxDocDisplayCount": 100}})
		if err != nil {
			return 0, 0, fmt.Errorf("encode search request: %w", err)
		}
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/search", bytes.NewReader(body))
		if err != nil {
			return 0, 0, fmt.Errorf("build search request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, fmt.Errorf("search request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Result struct {
				MatchCount int `json:"MatchCount"`
			} `json:"Result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return 0, 0, fmt.Errorf("decode search response: %w", err)
		}
		return out.Result.MatchCount, time.Since(start), nil
	}
	// Wait for the server to load shards.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, _, err := search("exemplar-warmup"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, errNotReady
		}
		time.Sleep(500 * time.Millisecond)
	}
	var stats []QueryStats
	for _, q := range queries {
		var lat []float64
		matches := 0
		for range repeats {
			n, d, err := search(q)
			if err != nil {
				return nil, fmt.Errorf("query %q: %w", q, err)
			}
			matches = n
			lat = append(lat, float64(d.Microseconds())/1000)
		}
		stats = append(stats, QueryStats{Query: q, Matches: matches, P50MS: Percentile(lat, 50), P95MS: Percentile(lat, 95), MaxMS: slices.Max(lat)})
	}
	return stats, nil
}

func ctagsCoverage(ctx context.Context, env []string, mirror, branch string, files []string, sample int, scratch string) []LanguageCoverage {
	byLang := map[string][]string{}
	for _, f := range files {
		if lang := LanguageOf(f); lang != "" {
			byLang[lang] = append(byLang[lang], f)
		}
	}
	_ = os.MkdirAll(scratch, 0o755)
	langs := []string{"Go", "C/C++", "TypeScript/JavaScript", "Python", "Java/Kotlin"}
	out := make([]LanguageCoverage, 0, len(langs))
	for _, lang := range langs {
		all := byLang[lang]
		lc := LanguageCoverage{Language: lang, FilesInRepo: len(all)}
		picked := EvenSample(all, sample)
		symbols := 0
		for _, f := range picked {
			data, err := outputBytes(ctx, env, mirror, "git", "show", branch+":"+f)
			if err != nil {
				continue
			}
			local := filepath.Join(scratch, "f"+filepath.Ext(f))
			if os.WriteFile(local, data, 0o600) != nil {
				continue
			}
			n := countTags(ctx, local)
			lc.Sampled++
			if n > 0 {
				lc.WithSymbols++
			}
			symbols += n
		}
		if lc.Sampled > 0 {
			lc.Coverage = float64(lc.WithSymbols) / float64(lc.Sampled)
			lc.SymbolsPerFile = float64(symbols) / float64(lc.Sampled)
		}
		out = append(out, lc)
	}
	return out
}

func countTags(ctx context.Context, path string) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ctags", "--output-format=json", "-f", "-", path).Output()
	if err != nil {
		return 0
	}
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), []byte(`"_type": "tag"`)) {
			n++
		}
	}
	return n
}

// treeStats lists files and their total size at branch in the mirror.
func treeStats(ctx context.Context, env []string, mirror, branch string) ([]string, int64, error) {
	out, err := gitOutput(ctx, env, mirror, "ls-tree", "-r", "-l", "--full-tree", branch)
	if err != nil {
		return nil, 0, err
	}
	var files []string
	var total int64
	for line := range strings.SplitSeq(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 || f[1] != "blob" {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		total += size
		files = append(files, path)
	}
	return files, total, nil
}

func size(r *Report) Sizing {
	working := r.Mirror.Bytes + 2*r.FullIndex.IndexBytes // serving + rebuilding generation
	disk := working + working/4
	s := Sizing{DiskBytesPerReplica: disk, RAMBytesForHotShards: r.FullIndex.IndexBytes}
	if r.Repository.Bytes > 0 {
		s.SourceMultiple = float64(disk) / float64(r.Repository.Bytes)
	}
	return s
}

// Percentile returns the p-th percentile (nearest rank) of values.
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := slices.Clone(values)
	slices.Sort(s)
	rank := int(p/100*float64(len(s))+0.999999) - 1
	return s[max(0, min(rank, len(s)-1))]
}

// EvenSample picks up to n items spread evenly across list.
func EvenSample(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := make([]string, 0, n)
	step := float64(len(list)) / float64(n)
	for i := range n {
		out = append(out, list[int(float64(i)*step)])
	}
	return out
}

func timed(ctx context.Context, env []string, name string, args ...string) (float64, error) {
	start := time.Now()
	_, err := outputBytes(ctx, env, "", name, args...)
	return time.Since(start).Seconds(), err
}

func gitOutput(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	b, err := outputBytes(ctx, env, dir, "git", args...)
	return strings.TrimSpace(string(b)), err
}

func outputBytes(ctx context.Context, env []string, dir, name string, args ...string) ([]byte, error) {
	if name == "git" && dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(redactArgs(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// redactArgs hides URL credentials in error messages.
func redactArgs(args []string) []string {
	out := slices.Clone(args)
	for i, a := range out {
		if u, err := url.Parse(a); err == nil && u.User != nil {
			u.User = url.User("redacted")
			out[i] = u.String()
		}
	}
	return out
}

func gitEnv(token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+token)
	}
	return env
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func countFiles(dir, ext string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ext {
			n++
		}
		return nil
	})
	return n
}
