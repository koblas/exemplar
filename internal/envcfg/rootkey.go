package envcfg

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// RootKeyFile names the setting that points at the installation's root key
// (the exemplar-root-key Secret). Every internal key that the operator does
// not supply explicitly is derived from it.
const RootKeyFile = "EXEMPLAR_ROOT_KEY_FILE"

// MinRootKeyBytes is the shortest root key accepted.
const MinRootKeyBytes = 32

// Labels for the derived keys. A label never changes meaning: rotating one
// key on its own is done with its explicit setting, not a new label.
const (
	LabelLink         = "exemplar/link/v1"
	LabelSession      = "exemplar/session/v1"
	LabelGatewayToken = "exemplar/gateway-token/v1" //nolint:gosec // G101: a KDF label, not a credential
	LabelSearchToken  = "exemplar/search-token/v1"  //nolint:gosec // G101: a KDF label, not a credential
	LabelPseudonym    = "exemplar/pseudonym/v1"
)

var (
	errNoKey        = errors.New("is required (or set " + RootKeyFile + ")")
	errShortRootKey = fmt.Errorf("%s must hold at least %d bytes", RootKeyFile, MinRootKeyBytes)
)

// Keys resolves the internal keys. An explicit setting wins; otherwise the
// key is derived from the root key with HKDF-SHA256 and the key's label, so
// every binary that reads the same root key derives the same value.
type Keys struct {
	getenv   func(string) string
	readFile func(string) ([]byte, error)

	once sync.Once
	root []byte
	err  error
}

// NewKeys reads settings with getenv and key files with readFile (os.Getenv
// and os.ReadFile outside tests). The root key is read on first use.
func NewKeys(getenv func(string) string, readFile func(string) ([]byte, error)) *Keys {
	return &Keys{getenv: getenv, readFile: readFile}
}

// File returns the key in the file that fileEnv names, trimmed, or the key
// derived for label when fileEnv is unset.
func (k *Keys) File(fileEnv, label string) ([]byte, error) {
	if p := k.getenv(fileEnv); p != "" {
		b, err := k.readFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fileEnv, err)
		}
		return bytes.TrimSpace(b), nil
	}
	b, err := k.derive(label)
	if err != nil {
		return nil, fmt.Errorf("%s %w", fileEnv, err)
	}
	return b, nil
}

// Token returns the value of env, or the key derived for label, hex-encoded,
// when env is unset. Tokens travel in headers and environment variables, so
// the derived ones are printable.
func (k *Keys) Token(env, label string) (string, error) {
	if v := k.getenv(env); v != "" {
		return v, nil
	}
	b, err := k.derive(label)
	if err != nil {
		return "", fmt.Errorf("%s %w", env, err)
	}
	return hex.EncodeToString(b), nil
}

func (k *Keys) derive(label string) ([]byte, error) {
	k.once.Do(func() {
		p := k.getenv(RootKeyFile)
		if p == "" {
			k.err = errNoKey
			return
		}
		b, err := k.readFile(p)
		if err != nil {
			k.err = fmt.Errorf("read %s: %w", RootKeyFile, err)
			return
		}
		if b = bytes.TrimSpace(b); len(b) < MinRootKeyBytes {
			k.err = errShortRootKey
			return
		}
		k.root = b
	})
	if k.err != nil {
		return nil, k.err
	}
	return Derive(k.root, label)
}

// Derive returns the 32-byte key for label: HKDF-SHA256 with no salt and the
// label as info. learning/src/exemplar_learning/rootkey.py implements the
// same derivation; both test the same vector.
func Derive(root []byte, label string) ([]byte, error) {
	b, err := hkdf.Key(sha256.New, root, nil, label, 32)
	if err != nil {
		return nil, fmt.Errorf("derive %s: %w", label, err)
	}
	return b, nil
}
