package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/setup"
)

// After setup, exemplar-api reads the values setup wrote where a restarted
// pod would find them, and everything else as before.
func TestEnvWith(t *testing.T) {
	base := env{
		get: func(k string) string {
			return map[string]string{
				"EXEMPLAR_API_URL":                          "https://exemplar.acme.com",
				"EXEMPLAR_GH_PRIVATE_KEY_FILE":              "/run/app/private-key",
				"EXEMPLAR_OAUTH2_GITHUB_CLIENT_SECRET_FILE": "/run/app/client-secret",
				envcfg.RootKeyFile:                          "/run/root/key",
			}[k]
		},
		read: func(p string) ([]byte, error) {
			if p == "/run/oidc/client-secret" {
				return []byte("oidc"), nil
			}
			return nil, fs.ErrNotExist
		},
	}
	e := base.with(&setup.Installed{
		AppID: 4242, ClientID: "Iv23li", ClientSecret: []byte("cs"), PrivateKey: []byte("pem"),
		RootKey: []byte("root"), DatabaseURL: "postgres://db/exemplar",
	})
	for k, want := range map[string]string{
		"EXEMPLAR_GH_APP_ID": "4242", "EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID": "Iv23li",
		"EXEMPLAR_DATABASE_URL": "postgres://db/exemplar", "EXEMPLAR_API_URL": "https://exemplar.acme.com",
	} {
		if got := e.get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for p, want := range map[string]string{
		"/run/app/private-key": "pem", "/run/app/client-secret": "cs", "/run/root/key": "root", "/run/oidc/client-secret": "oidc",
	} {
		if got, err := e.read(p); err != nil || string(got) != want {
			t.Errorf("read %s = %q, %v; want %q", p, got, err, want)
		}
	}
	if _, err := e.read("/elsewhere"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read of another path = %v", err)
	}

	// A database URL in the environment wins over setup's.
	withDB := env{get: func(k string) string {
		if k == "EXEMPLAR_DATABASE_URL" {
			return "postgres://external/exemplar"
		}
		return ""
	}, read: base.read}.with(&setup.Installed{DatabaseURL: "postgres://db/exemplar"})
	if got := withDB.get("EXEMPLAR_DATABASE_URL"); got != "postgres://external/exemplar" {
		t.Errorf("EXEMPLAR_DATABASE_URL = %q, want the environment's", got)
	}
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func secretsWith(data map[string]map[string][]byte) setup.Secrets {
	names := []string{
		"exemplar-setup", "exemplar-github-app", "exemplar-exports-app", "exemplar-root-key",
		"exemplar-postgres", "exemplar-database", "exemplar-providers",
	}
	objs := make([]runtime.Object, 0, len(names))
	for _, name := range names {
		objs = append(objs, &corev1.Secret{Name: name, Namespace: "exemplar", Data: data[name]})
	}
	return setup.KubeSecrets{Client: k8sfake.NewClientset(objs...), Namespace: "exemplar"}
}

func setupEnv(t *testing.T) {
	t.Helper()
	t.Setenv("EXEMPLAR_API_URL", "https://exemplar.acme.com")
	t.Setenv("EXEMPLAR_CONFIG_REPO", "acme/exemplar-config")
	t.Setenv("EXEMPLAR_DATABASE_URL", "postgres://external/exemplar") // no in-cluster Postgres lookup
}

// A pod in setup mode that finds setup complete in the Secrets (finished
// on another pod) leaves setup mode with what they hold.
func TestRunSetupLeavesWhenComplete(t *testing.T) {
	setupEnv(t)
	key := testKey(t)
	secrets := secretsWith(map[string]map[string][]byte{
		"exemplar-github-app": {
			"app-id": []byte("4242"), "private-key": key, "installation-id": []byte("77"),
			"client-id": []byte("Iv23li"), "client-secret": []byte("cs"), "webhook-secret": []byte("ws"),
		},
		"exemplar-root-key":  {"key": []byte("root")},
		"exemplar-providers": {"anthropic-api-key": []byte("sk-ant")},
	})
	in, err := runSetup(t.Context(), slog.New(slog.DiscardHandler), secrets, setupFlags{addr: "127.0.0.1:0", namespace: "exemplar"})
	if err != nil {
		t.Fatal(err)
	}
	if in == nil || in.AppID != 4242 || in.ClientID != "Iv23li" || string(in.PrivateKey) != string(key) || string(in.RootKey) != "root" {
		t.Fatalf("Installed = %+v", in)
	}
}

// Stopped before setup is complete, runSetup returns nothing to start.
func TestRunSetupStops(t *testing.T) {
	setupEnv(t)
	secrets := secretsWith(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	in, err := runSetup(ctx, slog.New(slog.DiscardHandler), secrets, setupFlags{addr: "127.0.0.1:0", namespace: "exemplar"})
	if err != nil || in != nil {
		t.Fatalf("runSetup after SIGTERM = %+v, %v", in, err)
	}
}
