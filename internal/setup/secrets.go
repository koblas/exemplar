package setup

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// downloadPath serves the one file the last step hands the operator: every
// Secret the installation still needs that setup can make. exemplar-api
// never writes Secrets itself; it has no Kubernetes API access, and
// RBAC can't narrow "create" to one Secret name.
const downloadPath = "/setup/exemplar-secrets.yaml"

// Database Secrets in the file (secretsPlan.Database).
const (
	dbGenerated = "generated" // in-cluster Postgres: a new password and its URL
	dbPresent   = "present"   // exemplar-api already has EXEMPLAR_DATABASE_URL
	dbExternal  = "external"  // no in-cluster Postgres: the operator brings the URL
	dbApplied   = "applied"   // a Secrets file from setup was applied since the pod started
)

// Models choices on the download form, and its in-place errors.
const (
	modelsAnthropic = "anthropic" // paste an Anthropic key: exemplar-providers in the file
	modelsOwn       = "own"       // exemplar-providers made elsewhere (Bedrock, a secret manager)

	providerKeyMissing = "key-missing"
	providerKeyInvalid = "key-invalid"
)

// secretsPlan is what the Secrets file holds besides the App Secrets,
// decided from what this pod can see.
type secretsPlan struct {
	RootKey  bool   // exemplar-root-key: none mounted yet
	Database string // dbGenerated, dbPresent, dbExternal or dbApplied
	// Applied: the root key was missing when the pod started and is mounted
	// now, so a Secrets file was applied. Its generated values are in the
	// cluster; another pod's would differ, and Postgres keeps the first
	// password it sees, so they're left out.
	Applied bool
}

// postgresResolves reports whether components/postgres's Service exists:
// a ClusterIP Service's name resolves before its pod is ready, which it
// can't be until its Secret is applied.
func postgresResolves(namespace string) func(context.Context) bool {
	host := "exemplar-postgres." + namespace + ".svc"
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		return err == nil && len(addrs) > 0
	}
}

// rootKeyMissing reports whether no root key is mounted.
func (h *Handler) rootKeyMissing() bool {
	if h.cfg.RootKeyFile == "" {
		return false
	}
	b, err := os.ReadFile(h.cfg.RootKeyFile)
	return err != nil || len(bytes.TrimSpace(b)) == 0
}

func (h *Handler) plan(ctx context.Context) secretsPlan {
	p := secretsPlan{RootKey: h.rootKeyMissing()}
	p.Applied = h.rootKeyMissingAtStart && !p.RootKey
	switch {
	case h.cfg.HasDatabase || mounted(h.cfg.DatabaseFile):
		p.Database = dbPresent
	case !h.cfg.InClusterPostgres(ctx):
		p.Database = dbExternal
	case p.Applied:
		p.Database = dbApplied // the kubelet may not show exemplar-database yet
	default:
		p.Database = dbGenerated
	}
	return p
}

// mounted reports whether path, in an optional Secret volume, has content.
func mounted(path string) bool {
	if path == "" {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && len(bytes.TrimSpace(b)) > 0
}

// download serves the Secrets file. The Anthropic key comes with the form
// and goes straight into the response: setup never keeps or logs it.
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("form")), []byte(h.form)) != 1 {
		h.failApp(ctx, w, http.StatusForbidden, failStaleForm)
		return
	}
	h.mu.Lock()
	review, exports := h.apps[KindReview], h.apps[KindExports]
	var installation int64
	var configMissing bool
	if review != nil {
		installation, configMissing = review.installation, review.configMissing
	}
	h.mu.Unlock()
	if review == nil || installation == 0 || configMissing {
		h.failApp(ctx, w, http.StatusConflict, failNotReady)
		return
	}
	var key string
	if r.PostFormValue("models") != modelsOwn {
		key = strings.TrimSpace(r.PostFormValue("anthropic-key"))
		switch {
		case key == "":
			h.renderStatus(w, r, http.StatusUnprocessableEntity, pageErrors{provider: providerKeyMissing})
			return
		case strings.IndexFunc(key, func(c rune) bool { return c > unicode.MaxASCII || !unicode.IsPrint(c) || c == ' ' }) >= 0:
			h.renderStatus(w, r, http.StatusUnprocessableEntity, pageErrors{provider: providerKeyInvalid})
			return
		}
	}
	var b strings.Builder
	p := h.plan(ctx)
	if p.RootKey {
		h.writeSecret(&b, "exemplar-root-key", [][2]string{{"key", h.rootKey}}, "")
	}
	if p.Database == dbGenerated {
		// components/postgres: user and database exemplar, port 5432. The
		// password is hex, so the URL needs no escaping.
		h.writeSecret(&b, "exemplar-postgres", [][2]string{{"password", h.pgPassword}}, "")
		h.writeSecret(&b, "exemplar-database", [][2]string{{"url", fmt.Sprintf(
			"postgres://exemplar:%s@exemplar-postgres.%s.svc:5432/exemplar", h.pgPassword, h.cfg.Namespace)}}, "")
	}
	if key != "" {
		h.writeSecret(&b, "exemplar-providers", [][2]string{{"anthropic-api-key", key}}, "")
	}
	h.writeSecret(&b, "exemplar-github-app", [][2]string{
		{"app-id", strconv.FormatInt(review.created.ID, 10)},
		{"installation-id", strconv.FormatInt(installation, 10)},
		{"client-id", review.created.ClientID},
		{"client-secret", review.created.ClientSecret},
		{"webhook-secret", review.created.WebhookSecret},
	}, review.created.PEM)
	if exports != nil {
		h.writeSecret(&b, "exemplar-exports-app", [][2]string{{"app-id", strconv.FormatInt(exports.created.ID, 10)}}, exports.created.PEM)
	}
	h.cfg.Log.Info("setup: Secrets file downloaded", "root_key", p.RootKey, "database", p.Database,
		"providers", key != "", "exports", exports != nil)
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="exemplar-secrets.yaml"`)
	_, _ = w.Write([]byte(b.String()))
}

// writeSecret appends one Secret document (the keys deploy/k8s reads),
// with privateKey, when set, as its private-key.
func (h *Handler) writeSecret(b *strings.Builder, name string, data [][2]string, privateKey string) {
	fmt.Fprintf(b, "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n  labels: {app.kubernetes.io/part-of: exemplar}\ntype: Opaque\nstringData:\n", name, h.cfg.Namespace)
	for _, kv := range data {
		fmt.Fprintf(b, "  %s: %s\n", kv[0], strconv.Quote(kv[1])) // printable ASCII only: ids, hex, URLs, checked keys
	}
	if privateKey == "" {
		return
	}
	b.WriteString("  private-key: |\n")
	for line := range strings.SplitSeq(strings.TrimRight(privateKey, "\n"), "\n") {
		fmt.Fprintf(b, "    %s\n", line)
	}
}
