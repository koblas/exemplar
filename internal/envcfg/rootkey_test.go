package envcfg

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// rootVector is the cross-language test vector: learning's rootkey tests
// expect the same hex for the same root and label.
const (
	rootVector    = "0123456789abcdef0123456789abcdef"
	gatewayVector = "c6650bbe7955f5ece892e1aa31cbda01ce4cc515c5bef1c407daf08860eb63f1"
)

func fakeEnv(env, files map[string]string) (func(string) string, func(string) ([]byte, error)) {
	return func(k string) string { return env[k] }, func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestDeriveVector(t *testing.T) {
	b, err := Derive([]byte(rootVector), LabelGatewayToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != gatewayVector {
		t.Fatalf("Derive = %s, want %s", got, gatewayVector)
	}
}

func TestKeys(t *testing.T) {
	root := strings.Repeat("r", 32)
	tests := []struct {
		name       string
		env, files map[string]string
		want       string // hex of File's result, or the Token
		wantErr    string
	}{
		{
			name:  "derived file key",
			env:   map[string]string{RootKeyFile: "/root"},
			files: map[string]string{"/root": root + "\n"},
			want:  mustDeriveHex(t, root, LabelLink),
		},
		{
			name:  "explicit file wins",
			env:   map[string]string{RootKeyFile: "/root", "EXEMPLAR_LINK_KEY_FILE": "/link"},
			files: map[string]string{"/root": root, "/link": " explicit-link-key \n"},
			want:  hex.EncodeToString([]byte("explicit-link-key")),
		},
		{
			name:    "neither",
			env:     map[string]string{},
			wantErr: "EXEMPLAR_LINK_KEY_FILE is required (or set EXEMPLAR_ROOT_KEY_FILE)",
		},
		{
			name:    "short root",
			env:     map[string]string{RootKeyFile: "/root"},
			files:   map[string]string{"/root": "short"},
			wantErr: "at least 32 bytes",
		},
		{
			name:    "missing explicit file",
			env:     map[string]string{"EXEMPLAR_LINK_KEY_FILE": "/nope"},
			wantErr: "read EXEMPLAR_LINK_KEY_FILE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := NewKeys(fakeEnv(tc.env, tc.files))
			b, err := k.File("EXEMPLAR_LINK_KEY_FILE", LabelLink)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("File = %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(b); got != tc.want {
				t.Fatalf("File = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTokens(t *testing.T) {
	root := strings.Repeat("r", 32)
	k := NewKeys(fakeEnv(map[string]string{RootKeyFile: "/root", "EXEMPLAR_SEARCH_TOKEN": "explicit"}, map[string]string{"/root": root}))
	gw, err := k.Token("EXEMPLAR_GATEWAY_TOKEN", LabelGatewayToken)
	if err != nil {
		t.Fatal(err)
	}
	if gw != mustDeriveHex(t, root, LabelGatewayToken) || len(gw) != 64 {
		t.Fatalf("gateway token = %q", gw)
	}
	if s, _ := k.Token("EXEMPLAR_SEARCH_TOKEN", LabelSearchToken); s != "explicit" {
		t.Fatalf("explicit token = %q", s)
	}
	// Different labels give different keys from one root.
	seen := map[string]string{}
	for _, l := range []string{LabelLink, LabelSession, LabelGatewayToken, LabelSearchToken, LabelPseudonym, LabelRunnerClaims} {
		h := mustDeriveHex(t, root, l)
		if prev, ok := seen[h]; ok {
			t.Fatalf("%s and %s derive the same key", l, prev)
		}
		seen[h] = l
	}
}

func TestRootReadOnce(t *testing.T) {
	reads := 0
	getenv := func(k string) string {
		if k == RootKeyFile {
			return "/root"
		}
		return ""
	}
	k := NewKeys(getenv, func(string) ([]byte, error) {
		reads++
		return nil, errors.New("boom")
	})
	for range 3 {
		if _, err := k.Token("UNSET", LabelPseudonym); err == nil {
			t.Fatal("want an error")
		}
	}
	if reads != 1 {
		t.Fatalf("root key read %d times, want 1", reads)
	}
}

func mustDeriveHex(t *testing.T, root, label string) string {
	t.Helper()
	b, err := Derive([]byte(root), label)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
