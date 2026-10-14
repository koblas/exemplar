package gh

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadSARIF(t *testing.T) {
	status := http.StatusAccepted
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/app/code-scanning/sarifs" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(status)
		if status == http.StatusAccepted {
			_, _ = w.Write([]byte(`{"id": "s-1", "url": "u"}`))
		} else {
			_, _ = w.Write([]byte(`{"message": "Advanced Security must be enabled for this repository to use code scanning."}`))
		}
	}))
	defer srv.Close()
	p := NewPoster(clientFor(t, srv))
	pr, _ := ParsePR("acme/app", 7, "abc1234")
	id, err := p.UploadSARIF(t.Context(), pr, []byte(`{"version":"2.1.0"}`))
	if err != nil || id != "s-1" || got["commit_sha"] != "abc1234" || got["ref"] != "refs/pull/7/head" {
		t.Fatalf("upload = %q, %v (%v)", id, err, got)
	}
	gz, _ := base64.StdEncoding.DecodeString(got["sarif"])
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != `{"version":"2.1.0"}` {
		t.Errorf("sarif = %q", plain)
	}
	status = http.StatusForbidden
	if _, err := p.UploadSARIF(t.Context(), pr, []byte(`{}`)); !errors.Is(err, ErrCodeScanningUnavailable) {
		t.Errorf("forbidden = %v", err)
	}
}

func TestCodeScanningAnnotations(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/acme/app/code-scanning/alerts" || r.URL.Query().Get("ref") != "refs/pull/7/merge" || r.URL.Query().Get("state") != "open" {
			t.Errorf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"message": "no analysis"}`))
			return
		}
		_, _ = w.Write([]byte(`[
		  {"number": 1, "rule": {"id": "go/sql-injection", "severity": "error"}, "tool": {"name": "CodeQL"},
		   "most_recent_instance": {"location": {"path": "db.go", "start_line": 4, "end_line": 6}, "message": {"text": "Query built from user input."}}},
		  {"number": 2, "rule": {"id": "x"}, "tool": {"name": "Other"}, "most_recent_instance": {"location": {"path": "a.go", "start_line": 1}}}]`))
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	anns, err := CodeScanningAnnotations(t.Context(), c, "acme/app", 7, []string{"codeql"})
	if err != nil || len(anns) != 1 || anns[0].Check != "CodeQL" || anns[0].Title != "go/sql-injection" || anns[0].End != 6 || anns[0].Message != "Query built from user input." {
		t.Fatalf("anns = %+v, %v", anns, err)
	}
	status = http.StatusNotFound
	if anns, err := CodeScanningAnnotations(t.Context(), c, "acme/app", 7, []string{"codeql"}); err != nil || anns != nil {
		t.Errorf("no code scanning = %+v, %v", anns, err)
	}
}
