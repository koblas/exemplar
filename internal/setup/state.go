package setup

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"time"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/gh"
)

// Setup's state lives in the Secrets it writes, not in this process: any
// exemplar-api pod answers any request, and a pod that restarts mid-flow
// picks up where the last one was. The App's credentials go into
// exemplar-github-app the moment GitHub hands them over.

// Keys setup reads and writes, by Secret.
const (
	keyToken     = "token"     // exemplar-setup: the bootstrap token the operator signs in with
	keySigning   = "key"       // exemplar-setup: signs setup's cookies, forms and manifest states
	keyProviders = "providers" // exemplar-setup: "own" when the operator manages exemplar-providers

	keyAppID         = "app-id"
	keyInstallation  = "installation-id"
	keyClientID      = "client-id"
	keyClientSecret  = "client-secret"
	keyWebhookSecret = "webhook-secret"
	keyPrivateKey    = "private-key"
	keySlug          = "slug"     // shown on the page; GitHub's answer replaces it on each load
	keyHTMLURL       = "html-url" // likewise

	keyRootKey     = "key"
	keyPassword    = "password"
	keyDatabaseURL = "url"
	keyAnthropic   = "anthropic-api-key"
)

// Models choices on the last step.
const (
	modelsAnthropic = "anthropic" // paste an Anthropic key: setup writes exemplar-providers
	modelsOwn       = "own"       // exemplar-providers made elsewhere (Bedrock, a secret manager)
)

// Database states on the last step.
const (
	dbReady    = "ready"    // exemplar-database has a URL (or exemplar-api's environment does)
	dbExternal = "external" // no in-cluster Postgres: the operator brings the URL
)

// Installed is what exemplar-api needs to leave setup mode, read from the
// Secrets setup wrote.
type Installed struct {
	AppID                    int64
	ClientID                 string
	ClientSecret, PrivateKey []byte
	RootKey                  []byte
	DatabaseURL              string // "" when exemplar-api's environment already has one
}

// snapshot is every Secret setup reads, as of one request.
type snapshot struct {
	setup, app, exports, rootKey, database, providers Secret
}

// getOptional reads name; a missing Secret reads as empty.
func (h *Handler) getOptional(ctx context.Context, name string) (Secret, error) {
	s, err := h.cfg.Secrets.Get(ctx, name)
	if errors.Is(err, errSecretMissing) {
		return Secret{Name: name}, nil
	}
	return s, err //nolint:wrapcheck // Secrets' errors name the Secret, and callers test them with errors.Is
}

func (h *Handler) load(ctx context.Context) (snapshot, error) {
	var s snapshot
	errs := make([]error, 0, 6)
	for name, dst := range map[string]*Secret{
		secretSetup: &s.setup, secretApp: &s.app, secretExports: &s.exports,
		secretRootKey: &s.rootKey, secretDatabase: &s.database, secretProviders: &s.providers,
	} {
		v, err := h.getOptional(ctx, name)
		*dst = v
		errs = append(errs, err)
	}
	return s, errors.Join(errs...)
}

// ensure fills name with what fill returns for it (nil: nothing to add),
// reading again when another pod wrote it first. It returns the Secret as
// written.
func (h *Handler) ensure(ctx context.Context, name string, fill func(Secret) map[string][]byte) (Secret, error) {
	for range 3 {
		s, err := h.cfg.Secrets.Get(ctx, name)
		if err != nil {
			return s, err //nolint:wrapcheck // as in getOptional
		}
		data := fill(s)
		if data == nil {
			return s, nil
		}
		err = h.cfg.Secrets.Patch(ctx, name, s.ResourceVersion, data)
		if errors.Is(err, errConflict) {
			continue
		}
		if err != nil {
			return s, err //nolint:wrapcheck // as in getOptional
		}
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		maps.Copy(s.Data, data)
		return s, nil
	}
	return Secret{Name: name}, fmt.Errorf("%w: %s, three times", errConflict, name)
}

// Prepare writes what the installation needs before anyone signs in: setup's
// token and signing key, and for the in-cluster Postgres its password and
// URL, so the database starts while the operator registers the App. Values
// already there stay: Postgres keeps the first password it initializes
// with. Safe to call from every pod, any number of times.
//
// The root key comes last, from Finish (writeRootKey): every other
// workload mounts it, so none starts before the App and the models are in
// their Secrets (the gateway reads exemplar-providers only as it starts).
func (h *Handler) Prepare(ctx context.Context) error {
	st, err := h.ensure(ctx, secretSetup, func(s Secret) map[string][]byte {
		if s.Has(keyToken) && s.Has(keySigning) {
			return nil
		}
		return map[string][]byte{keyToken: []byte(random()), keySigning: []byte(randomHex(32))}
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.signing = st.Data[keySigning]
	h.token = st.Data[keyToken]
	h.mu.Unlock()
	if h.cfg.HasDatabase || !h.cfg.InClusterPostgres(ctx) {
		return nil
	}
	pg, err := h.ensure(ctx, secretPostgres, func(s Secret) map[string][]byte {
		if s.Has(keyPassword) {
			return nil
		}
		return map[string][]byte{keyPassword: []byte(random())}
	})
	if err != nil {
		return err
	}
	// components/postgres: user and database exemplar, port 5432. The
	// password is hex, so the URL needs no escaping.
	dbURL := fmt.Sprintf("postgres://exemplar:%s@exemplar-postgres.%s.svc:5432/exemplar", pg.String(keyPassword), h.cfg.Namespace)
	_, err = h.ensure(ctx, secretDatabase, func(s Secret) map[string][]byte {
		if s.Has(keyDatabaseURL) {
			return nil
		}
		return map[string][]byte{keyDatabaseURL: []byte(dbURL)}
	})
	return err
}

// writeRootKey writes a root key unless there is one.
func (h *Handler) writeRootKey(ctx context.Context) error {
	_, err := h.ensure(ctx, secretRootKey, func(s Secret) map[string][]byte {
		if s.Has(keyRootKey) {
			return nil
		}
		return map[string][]byte{keyRootKey: []byte(randomHex(envcfg.MinRootKeyBytes))}
	})
	return err
}

// postgresResolves reports whether components/postgres's Service exists:
// a ClusterIP Service's name resolves before its pod is ready.
func postgresResolves(namespace string) func(context.Context) bool {
	host := "exemplar-postgres." + namespace + ".svc"
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		return err == nil && len(addrs) > 0
	}
}

// appFrom is the App a Secret holds, or nil when it holds none.
func (h *Handler) appFrom(s Secret) (*app, error) {
	if !s.Has(keyAppID) || !s.Has(keyPrivateKey) {
		return nil, nil //nolint:nilnil // no App yet is not an error
	}
	id, err := strconv.ParseInt(s.String(keyAppID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s app-id: %w", s.Name, err)
	}
	created := &gh.CreatedApp{
		ID: id, Slug: s.String(keySlug), HTMLURL: s.String(keyHTMLURL), PEM: s.String(keyPrivateKey),
		ClientID: s.String(keyClientID), ClientSecret: s.String(keyClientSecret), WebhookSecret: s.String(keyWebhookSecret),
	}
	client, err := gh.NewApp(gh.AppConfig{
		AppID: id, PrivateKey: s.Data[keyPrivateKey], ConfigRepo: h.cfg.ConfigRepo, BaseURL: h.cfg.GitHubAPI,
	}, h.cfg.HTTP.Transport)
	if err != nil {
		return nil, fmt.Errorf("%s private-key: %w", s.Name, err)
	}
	var installation int64
	if s.Has(keyInstallation) {
		installation, _ = strconv.ParseInt(s.String(keyInstallation), 10, 64)
	}
	return &app{created: created, client: client, installation: installation, secret: s}, nil
}

// appData is what a Secret holds for an App GitHub just created.
func appData(kind string, c *gh.CreatedApp) map[string][]byte {
	d := map[string][]byte{
		keyAppID: []byte(strconv.FormatInt(c.ID, 10)), keyPrivateKey: []byte(c.PEM),
		keySlug: []byte(c.Slug), keyHTMLURL: []byte(c.HTMLURL), keyInstallation: nil,
	}
	if kind == KindReview {
		d[keyClientID], d[keyClientSecret], d[keyWebhookSecret] = []byte(c.ClientID), []byte(c.ClientSecret), []byte(c.WebhookSecret)
	}
	return d
}

// cleared removes every key s holds.
func cleared(s Secret) map[string][]byte {
	d := map[string][]byte{}
	for k := range s.Data {
		d[k] = nil
	}
	return d
}

// databaseState is the last step's database state.
func (h *Handler) databaseState(s snapshot) string {
	if h.cfg.HasDatabase || s.database.Has(keyDatabaseURL) {
		return dbReady
	}
	return dbExternal
}

// providersChosen: an Anthropic key is in exemplar-providers, or the
// operator said they manage it.
func providersChosen(s snapshot) bool {
	return len(s.providers.Data) > 0 || s.setup.String(keyProviders) == modelsOwn
}

// installed is what exemplar-api starts with, once every step is done:
// the App installed with the config repo, a root key, a database, and a
// models choice.
func (h *Handler) installed(s snapshot, review *app) *Installed {
	if review == nil || review.installation == 0 || !s.rootKey.Has(keyRootKey) ||
		h.databaseState(s) != dbReady || !providersChosen(s) {
		return nil
	}
	return &Installed{
		AppID: review.created.ID, ClientID: review.created.ClientID,
		ClientSecret: []byte(review.created.ClientSecret), PrivateKey: []byte(review.created.PEM),
		RootKey: s.rootKey.Data[keyRootKey], DatabaseURL: s.database.String(keyDatabaseURL),
	}
}

// Installed reports what exemplar-api needs to leave setup mode, or nil
// while a step is left. It reads the Secrets, so every pod sees the same.
func (h *Handler) Installed(ctx context.Context) (*Installed, error) {
	s, err := h.load(ctx)
	if err != nil {
		return nil, err
	}
	review, err := h.appFrom(s.app)
	if err != nil {
		return nil, err
	}
	return h.installed(s, review), nil
}
