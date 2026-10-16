// Package api is exemplar-search's internal HTTP/JSON API
// and its client. It serves the mirror (snapshot, read_file at a commit,
// git log for path_history, fetch requests relayed from push webhooks) and
// the zoekt index (search_text, find_definition; git grep in degraded
// mode). find_references and dependents come next.
//
// Every call names the PR's repo (pr_repo) and the repo to read. The
// server allows only the PR's own repo and its context.extra_repos in the
// same visibility tier, and refuses denylisted repos.
// Callers authenticate with a shared bearer token; NetworkPolicy limits
// who can connect at all.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/search/index"
	"github.com/koblas/exemplar/internal/search/mirror"
)

const maxBody = 64 << 10

// Error codes in error responses; the client maps them back to the
// mirror package's errors.
const (
	CodeBadRequest  = "bad_request"
	CodeForbidden   = "forbidden"
	CodeNotMirrored = "not_mirrored"
	CodeNoCommit    = "no_commit"
	CodeNoFile      = "no_file"
	CodeNotInScope  = "not_in_scope"
	CodeInternal    = "internal"
)

// Configuration errors from NewServer.
var (
	errServerDeps = errors.New("api: manager, scheduler and snapshot are required")
	errShortToken = errors.New("api: the API token must be at least 32 bytes")
)

// Server serves the API.
type Server struct {
	m        *mirror.Manager
	sched    *mirror.Scheduler
	snap     func() *config.Snapshot
	token    string
	log      *slog.Logger
	draining atomic.Bool
	index    *index.Index
}

// NewServer returns a Server. snap returns the current exemplar-config
// snapshot. token is required.
func NewServer(m *mirror.Manager, sched *mirror.Scheduler, snap func() *config.Snapshot, token string, log *slog.Logger) (*Server, error) {
	if m == nil || sched == nil || snap == nil {
		return nil, errServerDeps
	}
	if len(token) < 32 {
		return nil, errShortToken
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{m: m, sched: sched, snap: snap, token: token, log: log}, nil
}

// WithIndex serves search_text from ix.
func (s *Server) WithIndex(ix *index.Index) *Server {
	s.index = ix
	return s
}

// Drain makes /readyz fail, for graceful shutdown.
func (s *Server) Drain() { s.draining.Store(true) }

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Liveness does not depend on GitHub or the mirrors.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /v1/status", s.auth(http.HandlerFunc(s.status)))
	mux.Handle("POST /v1/fetch", s.auth(http.HandlerFunc(s.fetch)))
	mux.Handle("POST /v1/snapshot", s.auth(http.HandlerFunc(s.snapshot)))
	mux.Handle("POST /v1/read_file", s.auth(http.HandlerFunc(s.readFile)))
	mux.Handle("POST /v1/log", s.auth(http.HandlerFunc(s.gitLog)))
	mux.Handle("POST /v1/search_text", s.auth(http.HandlerFunc(s.searchText)))
	mux.Handle("POST /v1/find_definition", s.auth(http.HandlerFunc(s.findDefinition)))
	mux.Handle("POST /v1/find_references", s.auth(http.HandlerFunc(s.findReferences)))
	mux.Handle("POST /v1/dependents", s.auth(http.HandlerFunc(s.dependents)))
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or wrong token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readyz is ready when not draining and every repo in scope has a mirror
// with a default branch. A restart with mirrors on disk is ready at once.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeErr(w, http.StatusServiceUnavailable, "draining", "shutting down")
		return
	}
	var missing []string
	for _, repo := range mirror.InScope(s.snap()) {
		if _, err := s.m.Snapshot(r.Context(), repo); err != nil {
			missing = append(missing, repo)
		}
	}
	if len(missing) > 0 {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "not yet mirrored: "+strings.Join(missing, ", "))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"repos": s.sched.Status()}
	if s.index != nil {
		var idx []index.Status //nolint:prealloc // nil encodes as null for an empty scope; keep the status wire format
		for _, r := range mirror.InScope(s.snap()) {
			idx = append(idx, s.index.Status(r))
		}
		out["index"] = idx
	}
	writeJSON(w, http.StatusOK, out)
}

// FetchRequest asks for a repo to be fetched (a relayed push webhook).
type FetchRequest struct {
	Repo string `json:"repo"`
}

func (s *Server) fetch(w http.ResponseWriter, r *http.Request) {
	var req FetchRequest
	if !decode(w, r, &req) {
		return
	}
	if !s.sched.Request(req.Repo) { //nolint:contextcheck // the fetch outlives this request; it runs under the scheduler's context
		writeErr(w, http.StatusNotFound, CodeNotInScope, "repo is not in scope")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

// Target names the PR's repo and the repo to read.
type Target struct {
	PRRepo string `json:"pr_repo"`
	Repo   string `json:"repo"`
}

// SnapshotResponse carries a repo's current default-branch commit, and
// whether search for it is in degraded mode (a review that pins
// this snapshot skips bug.contract).
type SnapshotResponse struct {
	SnapshotSHA string `json:"snapshot_sha"`
	Degraded    bool   `json:"degraded"`
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	var req Target
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req) {
		return
	}
	sha, err := s.m.Snapshot(r.Context(), req.Repo)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SnapshotResponse{SnapshotSHA: sha, Degraded: s.index == nil || s.index.Status(req.Repo).Degraded})
}

// ReadFileRequest is read_file(commit, path, start, end).
type ReadFileRequest struct {
	Target

	Commit string `json:"commit"`
	Path   string `json:"path"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// ReadFileResponse is a file slice and the snapshot the server is at.
type ReadFileResponse struct {
	mirror.File

	SnapshotSHA string `json:"snapshot_sha"`
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	var req ReadFileRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	f, err := s.m.ReadFile(r.Context(), req.Repo, req.Commit, req.Path, req.Start, req.End)
	if err != nil {
		s.fail(w, err)
		return
	}
	sha, _ := s.m.Snapshot(r.Context(), req.Repo)
	writeJSON(w, http.StatusOK, ReadFileResponse{File: f, SnapshotSHA: sha})
}

// LogRequest asks for a path's recent commits as of a commit.
type LogRequest struct {
	Target

	Commit string `json:"commit"`
	Path   string `json:"path"`
	N      int    `json:"n"`
}

// LogResponse lists commits newest first.
type LogResponse struct {
	Commits     []mirror.Commit `json:"commits"`
	SnapshotSHA string          `json:"snapshot_sha"`
}

func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	var req LogRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	commits, err := s.m.Log(r.Context(), req.Repo, req.Commit, req.Path, req.N)
	if err != nil {
		s.fail(w, err)
		return
	}
	sha, _ := s.m.Snapshot(r.Context(), req.Repo)
	writeJSON(w, http.StatusOK, LogResponse{Commits: commits, SnapshotSHA: sha})
}

// SearchTextRequest is search_text(pattern, path_filter?, lang?).
type SearchTextRequest struct {
	Target

	Pattern    string `json:"pattern"`
	PathFilter string `json:"path_filter,omitempty"`
	Lang       string `json:"lang,omitempty"`
	// Snapshot is the review's pinned snapshot_sha, if any.
	Snapshot string `json:"snapshot,omitempty"`
}

// SearchTextResponse is a search answer. SnapshotMismatch is set when the
// answer came from a different commit than the pinned snapshot.
type SearchTextResponse struct {
	index.Result

	SnapshotSHA      string `json:"snapshot_sha"`
	SnapshotMismatch bool   `json:"snapshot_mismatch"`
}

func (s *Server) searchText(w http.ResponseWriter, r *http.Request) {
	var req SearchTextRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	if s.index == nil {
		writeErr(w, http.StatusServiceUnavailable, "not_indexed", "this server has no index")
		return
	}
	res, err := s.index.SearchText(r.Context(), req.Repo, index.TextQuery{Pattern: req.Pattern, PathFilter: req.PathFilter, Lang: req.Lang})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SearchTextResponse{
		Result: res, SnapshotSHA: res.Commit,
		SnapshotMismatch: req.Snapshot != "" && req.Snapshot != res.Commit,
	})
}

// FindDefinitionRequest is find_definition(name, near_path?).
type FindDefinitionRequest struct {
	Target

	Name     string `json:"name"`
	NearPath string `json:"near_path,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
}

// FindDefinitionResponse lists up to 10 ranked definitions.
type FindDefinitionResponse struct {
	index.DefResult

	SnapshotSHA      string `json:"snapshot_sha"`
	SnapshotMismatch bool   `json:"snapshot_mismatch"`
}

func (s *Server) findDefinition(w http.ResponseWriter, r *http.Request) {
	var req FindDefinitionRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	if s.index == nil {
		writeErr(w, http.StatusServiceUnavailable, "not_indexed", "this server has no index")
		return
	}
	res, err := s.index.FindDefinition(r.Context(), req.Repo, index.DefQuery{Name: req.Name, NearPath: req.NearPath})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, FindDefinitionResponse{
		DefResult: res, SnapshotSHA: res.Commit,
		SnapshotMismatch: req.Snapshot != "" && req.Snapshot != res.Commit,
	})
}

// FindReferencesRequest is find_references(name, defined_in).
type FindReferencesRequest struct {
	Target

	Name      string `json:"name"`
	DefinedIn string `json:"defined_in,omitempty"`
	Snapshot  string `json:"snapshot,omitempty"`
}

// FindReferencesResponse lists up to 50 reference sites.
type FindReferencesResponse struct {
	index.RefResult

	SnapshotSHA      string `json:"snapshot_sha"`
	SnapshotMismatch bool   `json:"snapshot_mismatch"`
}

func (s *Server) findReferences(w http.ResponseWriter, r *http.Request) {
	var req FindReferencesRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	if s.index == nil {
		writeErr(w, http.StatusServiceUnavailable, "not_indexed", "this server has no index")
		return
	}
	roots, depth := s.repoContext(req.Repo)
	res, err := s.index.FindReferences(r.Context(), req.Repo, index.RefQuery{Name: req.Name, DefinedIn: req.DefinedIn, IncludeRoots: roots, Depth: depth})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, FindReferencesResponse{
		RefResult: res, SnapshotSHA: res.Commit,
		SnapshotMismatch: req.Snapshot != "" && req.Snapshot != res.Commit,
	})
}

// DependentsRequest is dependents(path, depth?).
type DependentsRequest struct {
	Target

	Path     string `json:"path"`
	Depth    int    `json:"depth,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
}

// DependentsResponse lists importing files and packages.
type DependentsResponse struct {
	index.DependentsResult

	SnapshotSHA      string `json:"snapshot_sha"`
	SnapshotMismatch bool   `json:"snapshot_mismatch"`
}

func (s *Server) dependents(w http.ResponseWriter, r *http.Request) {
	var req DependentsRequest
	if !decode(w, r, &req) || !s.allow(r.Context(), w, req.Target) {
		return
	}
	if s.index == nil {
		writeErr(w, http.StatusServiceUnavailable, "not_indexed", "this server has no index")
		return
	}
	roots, depth := s.repoContext(req.Repo)
	if req.Depth > 0 {
		depth = req.Depth
	}
	res, err := s.index.Dependents(r.Context(), req.Repo, req.Path, depth, roots)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DependentsResponse{
		DependentsResult: res, SnapshotSHA: res.Commit,
		SnapshotMismatch: req.Snapshot != "" && req.Snapshot != res.Commit,
	})
}

// repoContext is the searched repo's context.include_roots and
// dependents_depth from config.
func (s *Server) repoContext(repo string) ([]string, int) {
	for name, rc := range s.snap().Repos {
		if strings.EqualFold(name, repo) && rc.Context != nil {
			return rc.Context.IncludeRoots, rc.Context.DependentsDepth
		}
	}
	return nil, 0
}

// allow enforces scope and visibility tier, writing the refusal.
func (s *Server) allow(ctx context.Context, w http.ResponseWriter, t Target) bool {
	if err := mirror.Allowed(s.snap(), t.PRRepo, t.Repo); err != nil {
		writeErr(w, http.StatusForbidden, CodeForbidden, err.Error())
		return false
	}
	if strings.EqualFold(t.PRRepo, t.Repo) {
		return true
	}
	pv, err1 := s.m.Visibility(ctx, t.PRRepo)
	rv, err2 := s.m.Visibility(ctx, t.Repo)
	if err1 != nil || err2 != nil || !mirror.SameTier(pv, rv) {
		// Unknown visibility fails closed.
		writeErr(w, http.StatusForbidden, CodeForbidden, fmt.Sprintf("%s is not in %s's visibility tier", t.Repo, t.PRRepo))
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mirror.ErrBadRequest):
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, mirror.ErrNotMirrored):
		writeErr(w, http.StatusNotFound, CodeNotMirrored, err.Error())
	case errors.Is(err, mirror.ErrNoCommit):
		writeErr(w, http.StatusNotFound, CodeNoCommit, err.Error())
	case errors.Is(err, mirror.ErrNoFile):
		writeErr(w, http.StatusNotFound, CodeNoFile, err.Error())
	default:
		s.log.Error("search api error", "err", err)
		writeErr(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return false
	}
	return true
}

// ErrorResponse is every error's body.
type ErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, ErrorResponse{Code: code, Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // the status is already sent; a failed body write has no one to report to
}
