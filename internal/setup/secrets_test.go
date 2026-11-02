package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type secretDoc struct {
	Kind     string            `yaml:"kind"`
	Metadata map[string]any    `yaml:"metadata"`
	Data     map[string]string `yaml:"stringData"`
}

// secretsIn parses the Secrets file, by Secret name.
func secretsIn(t *testing.T, body string) map[string]secretDoc {
	t.Helper()
	out := map[string]secretDoc{}
	dec := yaml.NewDecoder(strings.NewReader(body))
	for {
		var d secretDoc
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("secrets file: %v\n%s", err, body)
		}
		if d.Kind != "Secret" || d.Metadata["namespace"] != "exemplar" {
			t.Errorf("document %+v", d)
		}
		out[fmt.Sprint(d.Metadata["name"])] = d
	}
}

// downloadSecrets posts the last step's form, with the page's token.
func downloadSecrets(t *testing.T, srv string, c *http.Client, form url.Values) page {
	t.Helper()
	m := formRe.FindStringSubmatch(get(t, c, srv+"/setup").body)
	if m == nil {
		m = []string{"", "none"}
	}
	form.Set("form", m[1])
	return do(t, c, http.MethodPost, srv+"/setup/exemplar-secrets.yaml", form)
}

// installed walks a browser through creating and installing the review App.
func installed(t *testing.T, srv string, fake *fakeGitHub) *http.Client {
	t.Helper()
	c := browser(t)
	createReview(t, srv, c)
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	if p := checkInstall(t, srv, c); p.status != http.StatusSeeOther {
		t.Fatalf("check installation = %d", p.status)
	}
	return c
}

// The file holds what the pod can see is missing, and nothing else.
func TestSecretsFileContents(t *testing.T) {
	rootKey := func(c *Config) {
		c.RootKeyFile = filepath.Join(t.TempDir(), "key")
		if err := os.WriteFile(c.RootKeyFile, []byte(strings.Repeat("k", 64)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		edit   func(*Config)
		models string
		want   []string
		says   string
	}{
		{
			name: "fresh install", models: modelsAnthropic,
			want: []string{"exemplar-root-key", "exemplar-postgres", "exemplar-database", "exemplar-providers", "exemplar-github-app"},
			says: "a new password for the in-cluster Postgres",
		},
		{
			name: "database set", edit: func(c *Config) { c.HasDatabase = true }, models: modelsAnthropic,
			want: []string{"exemplar-root-key", "exemplar-providers", "exemplar-github-app"},
			says: "not included, exemplar-api already has a database URL",
		},
		{
			name: "root key mounted", edit: rootKey, models: modelsAnthropic,
			want: []string{"exemplar-postgres", "exemplar-database", "exemplar-providers", "exemplar-github-app"},
			says: "not included, exemplar-api already has one",
		},
		{
			name: "providers made elsewhere", models: modelsOwn,
			want: []string{"exemplar-root-key", "exemplar-postgres", "exemplar-database", "exemplar-github-app"},
		},
		{
			name: "external database missing", edit: func(c *Config) { c.InClusterPostgres = func(context.Context) bool { return false } },
			models: modelsAnthropic, want: []string{"exemplar-root-key", "exemplar-providers", "exemplar-github-app"},
			says: "exemplar-api has no database yet, and this installation has no in-cluster Postgres",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fake := newSetupWith(t, tc.edit)
			c := installed(t, srv.URL, fake)
			if tc.says != "" {
				mustContain(t, get(t, c, srv.URL+"/setup"), tc.says)
			}
			p := downloadSecrets(t, srv.URL, c, url.Values{"models": {tc.models}, "anthropic-key": {" sk-ant-api03-abc_DEF-123 "}})
			if p.status != http.StatusOK || p.header.Get("Content-Disposition") != `attachment; filename="exemplar-secrets.yaml"` {
				t.Fatalf("download = %d %v\n%s", p.status, p.header, p.body)
			}
			got := secretsIn(t, p.body)
			if len(got) != len(tc.want) {
				t.Errorf("file holds %d Secrets, want %v", len(got), tc.want)
			}
			for _, name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Errorf("no %s in the file", name)
				}
			}
			if k, ok := got["exemplar-providers"]; ok && k.Data["anthropic-api-key"] != "sk-ant-api03-abc_DEF-123" {
				t.Errorf("anthropic key = %q, want it trimmed", k.Data["anthropic-api-key"])
			}
			if r, ok := got["exemplar-root-key"]; ok && len(r.Data["key"]) < 64 {
				t.Errorf("root key %q is under 32 bytes of entropy", r.Data["key"])
			}
			if d, ok := got["exemplar-database"]; ok {
				want := "postgres://exemplar:" + got["exemplar-postgres"].Data["password"] + "@exemplar-postgres.exemplar.svc:5432/exemplar"
				if d.Data["url"] != want {
					t.Errorf("database url = %q, want %q", d.Data["url"], want)
				}
			}
		})
	}
}

// Postgres keeps the first password it sees, so every download from one
// pod carries the same generated values.
func TestSecretsFileStable(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv.URL, fake)
	form := func() url.Values { return url.Values{"models": {modelsOwn}} }
	a, b := downloadSecrets(t, srv.URL, c, form()), downloadSecrets(t, srv.URL, c, form())
	if a.status != http.StatusOK || a.body != b.body {
		t.Errorf("two downloads differ:\n%s\n---\n%s", a.body, b.body)
	}
}

// The key goes into the download and nowhere else; a missing or mangled one
// is said in place, on the last step, without echoing it.
func TestProviderKey(t *testing.T) {
	var logs bytes.Buffer
	srv, fake := newSetupWith(t, func(c *Config) { c.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	c := installed(t, srv.URL, fake)

	p := downloadSecrets(t, srv.URL, c, url.Values{"models": {modelsAnthropic}, "anthropic-key": {"  "}})
	if p.status != http.StatusUnprocessableEntity || currentStep(t, p) != titleFinish {
		t.Fatalf("missing key = %d", p.status)
	}
	mustContain(t, p, "Paste your Anthropic API key, or choose that you've created exemplar-providers yourself.")

	const mangled = "sk-ant secret-from-a-paste"
	p = downloadSecrets(t, srv.URL, c, url.Values{"models": {modelsAnthropic}, "anthropic-key": {mangled}})
	if p.status != http.StatusUnprocessableEntity {
		t.Fatalf("mangled key = %d", p.status)
	}
	mustContain(t, p, "That doesn't look like an API key")
	mustNotContain(t, p, "secret-from-a-paste")

	const key = "sk-ant-api03-only-in-the-file"
	if p := downloadSecrets(t, srv.URL, c, url.Values{"models": {modelsAnthropic}, "anthropic-key": {key}}); !strings.Contains(p.body, key) {
		t.Fatalf("key not in the file:\n%s", p.body)
	}
	mustNotContain(t, get(t, c, srv.URL+"/setup"), key)
	if strings.Contains(logs.String(), key) || strings.Contains(logs.String(), "secret-from-a-paste") {
		t.Errorf("the key reached the log:\n%s", logs.String())
	}
}

// Every Secret the file holds is one a workload in deploy/k8s reads, with
// each key it requires (env refs and volume items not marked optional).
func TestSecretsFileMatchesManifests(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv.URL, fake)
	state := mustState(t, get(t, c, srv.URL+"/setup").body) // the exports app, too
	if p := get(t, c, srv.URL+"/setup/created?code=exports&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("exports created = %d", p.status)
	}
	file := secretsIn(t, downloadSecrets(t, srv.URL, c, url.Values{"anthropic-key": {"sk-ant-x"}}).body)
	if len(file) != 6 {
		t.Fatalf("file holds %d Secrets", len(file))
	}

	refs := map[string]map[string]bool{} // Secret name → keys required; "" for a whole-Secret mount
	read := map[string]map[string]bool{} // every key read, optional ones too
	mark := func(m map[string]map[string]bool, name, key string) {
		if m[name] == nil {
			m[name] = map[string]bool{}
		}
		m[name][key] = true
	}
	add := func(name, key string) { mark(refs, name, key) }
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["secretKeyRef"].(map[string]any); ok {
				mark(read, fmt.Sprint(ref["name"]), fmt.Sprint(ref["key"]))
				if ref["optional"] != true {
					add(fmt.Sprint(ref["name"]), fmt.Sprint(ref["key"]))
				}
			}
			if sec, ok := v["secret"].(map[string]any); ok && sec["secretName"] != nil {
				name := fmt.Sprint(sec["secretName"])
				if _, ok := sec["items"]; !ok {
					add(name, "") // the whole Secret
				}
				if items, ok := sec["items"].([]any); ok && sec["optional"] != true {
					for _, it := range items {
						if item, ok := it.(map[string]any); ok {
							add(name, fmt.Sprint(item["key"]))
						}
					}
				}
				if items, ok := sec["items"].([]any); ok {
					for _, it := range items {
						if item, ok := it.(map[string]any); ok {
							mark(read, name, fmt.Sprint(item["key"]))
						}
					}
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	for _, path := range manifestFiles(t) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		for {
			var doc any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			walk(doc)
		}
	}
	for name, s := range file {
		if len(read[name]) == 0 && !refs[name][""] {
			t.Errorf("%s: no workload reads it", name)
		}
		for key := range s.Data {
			// Whole-Secret mounts (root key, Postgres password) read every key.
			if !read[name][key] && !refs[name][""] {
				t.Errorf("%s has %s, which no workload in deploy/k8s reads", name, key)
			}
		}
		for key := range refs[name] {
			if _, ok := s.Data[key]; !ok && key != "" {
				t.Errorf("%s has no %s, which deploy/k8s reads", name, key)
			}
		}
	}
}

func manifestFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir("../../deploy/k8s", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(path) == ".yaml" {
			out = append(out, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Once a Secrets file from setup is applied, the root key's volume fills
// in, and a second download (another pod's, or for the exports app)
// leaves the generated Secrets out: Postgres already has the first password.
func TestSecretsFileAfterApply(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key")
	srv, fake := newSetupWith(t, func(c *Config) { c.RootKeyFile = keyFile })
	c := installed(t, srv.URL, fake)
	first := secretsIn(t, downloadSecrets(t, srv.URL, c, url.Values{"models": {modelsOwn}}).body)
	if _, ok := first["exemplar-postgres"]; !ok {
		t.Fatal("first download without the Postgres password")
	}
	if err := os.WriteFile(keyFile, []byte(first["exemplar-root-key"].Data["key"]), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, get(t, c, srv.URL+"/setup"), "not included, you've applied them from setup already")
	second := secretsIn(t, downloadSecrets(t, srv.URL, c, url.Values{"models": {modelsOwn}}).body)
	if len(second) != 1 || second["exemplar-github-app"].Kind != "Secret" {
		t.Errorf("after apply the file holds %v, want only exemplar-github-app", second)
	}
}
