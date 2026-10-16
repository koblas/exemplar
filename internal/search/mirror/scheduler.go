package mirror

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Schedule defaults (spec §6.4.1).
const (
	DefaultDebounce = 30 * time.Second
	DefaultBackstop = 5 * time.Minute
)

// Scheduler fetches mirrors: on request (a relayed push), at most once per
// Debounce per repo, and every Backstop for every repo in scope.
type Scheduler struct {
	m        *Manager
	scope    func() []string
	debounce time.Duration
	backstop time.Duration
	log      *slog.Logger
	now      func() time.Time
	// AfterFetch, when set, runs after each successful fetch in the same
	// goroutine, so work on one repo (indexing) never overlaps its fetches.
	AfterFetch func(ctx context.Context, repo, sha string)

	mu      sync.Mutex
	state   map[string]*fetchState
	ctx     context.Context //nolint:containedctx // Run's lifetime context, which the fetches Request starts run under
	wg      sync.WaitGroup
	stopped bool
}

type fetchState struct {
	last    time.Time // when the last fetch finished
	running bool
	again   bool // a request arrived while running
	timer   *time.Timer
	ok      bool // the last fetch succeeded
	err     string
	sha     string
}

// NewScheduler returns a Scheduler over m. scope lists the repos in scope
// and is called on every backstop tick, so config changes take effect.
func NewScheduler(m *Manager, scope func() []string, debounce, backstop time.Duration, log *slog.Logger) *Scheduler {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	if backstop <= 0 {
		backstop = DefaultBackstop
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{m: m, scope: scope, debounce: debounce, backstop: backstop, log: log, now: time.Now, state: map[string]*fetchState{}}
}

// Run fetches every repo in scope now and on each backstop tick until ctx
// ends, then waits for fetches in flight (they see ctx canceled).
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	t := time.NewTicker(s.backstop)
	defer t.Stop()
	for {
		for _, r := range s.scope() {
			s.Request(r) //nolint:contextcheck // fetches run under Run's lifetime context, not the caller's
		}
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopped = true
			for _, st := range s.state {
				if st.timer != nil {
					st.timer.Stop()
				}
			}
			s.mu.Unlock()
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// Request asks for repo to be fetched. It returns false when repo is not
// in scope. Requests within the debounce window coalesce into one fetch.
func (s *Scheduler) Request(repo string) bool {
	repo = strings.ToLower(repo)
	if !slices.Contains(s.scope(), repo) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.stopped {
		return true // accepted; the first Run pass fetches everything
	}
	st := s.state[repo]
	if st == nil {
		st = &fetchState{}
		s.state[repo] = st
	}
	switch {
	case st.running:
		st.again = true
	case st.timer != nil:
		// Already scheduled.
	case !st.last.IsZero() && s.now().Sub(st.last) < s.debounce:
		wait := st.last.Add(s.debounce).Sub(s.now())
		st.timer = time.AfterFunc(wait, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			st.timer = nil
			if !s.stopped {
				s.startLocked(repo, st)
			}
		})
	default:
		s.startLocked(repo, st)
	}
	return true
}

func (s *Scheduler) startLocked(repo string, st *fetchState) {
	st.running = true
	s.wg.Add(1)
	ctx := s.ctx
	go func() {
		defer s.wg.Done()
		start := time.Now()
		sha, err := s.m.Fetch(ctx, repo)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("mirror fetch failed", "repo", repo, "err", err)
			}
		} else {
			s.log.Info("mirror fetched", "repo", repo, "snapshot_sha", sha, "duration", time.Since(start))
			// Still marked running: no fetch of this repo can move the
			// branch while AfterFetch works on it.
			if s.AfterFetch != nil && ctx.Err() == nil {
				s.AfterFetch(ctx, repo, sha)
			}
		}
		s.mu.Lock()
		st.running, st.last = false, s.now()
		st.ok, st.sha, st.err = err == nil, sha, ""
		if err != nil {
			st.err = err.Error()
		}
		again := st.again && !s.stopped
		st.again = false
		s.mu.Unlock()
		if again {
			s.Request(repo) //nolint:contextcheck // fetches run under Run's lifetime context, not the caller's
		}
	}()
}

// RepoStatus is one repo's fetch state.
type RepoStatus struct {
	Repo        string    `json:"repo"`
	SnapshotSHA string    `json:"snapshot_sha,omitempty"`
	LastFetch   time.Time `json:"last_fetch,omitzero"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
}

// Status reports every repo in scope.
func (s *Scheduler) Status() []RepoStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RepoStatus //nolint:prealloc // nil encodes as null for an empty scope; keep the status wire format
	for _, r := range s.scope() {
		rs := RepoStatus{Repo: r}
		if st := s.state[r]; st != nil {
			rs.SnapshotSHA, rs.LastFetch, rs.OK, rs.Error = st.sha, st.last, st.ok, st.err
		}
		out = append(out, rs)
	}
	return out
}
