package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// The Secrets setup writes. deploy/k8s declares each one empty, so
// exemplar-api's Role can name them: RBAC can't narrow "create" to a name,
// but it can narrow get and patch.
//
//nolint:gosec // G101: Secret names, not credentials
const (
	secretSetup     = "exemplar-setup"       // token, key: setup's own sign-in and signing key; providers
	secretApp       = "exemplar-github-app"  // the review app
	secretExports   = "exemplar-exports-app" // the exports app, optional
	secretRootKey   = "exemplar-root-key"    // key
	secretPostgres  = "exemplar-postgres"    // password: components/postgres
	secretDatabase  = "exemplar-database"    // url
	secretProviders = "exemplar-providers"   // anthropic-api-key, or whatever the operator puts there
)

var (
	// errConflict: the Secret changed since it was read (another
	// exemplar-api pod wrote it); read it again.
	errConflict = errors.New("setup: Secret changed since it was read")
	// errSecretMissing: the Secret isn't in the cluster; the manifests
	// declare it, so they weren't all applied.
	errSecretMissing = errors.New("setup: Secret missing")
)

// Secret is one Secret's data and the version it was read at.
type Secret struct {
	Name            string
	ResourceVersion string
	Data            map[string][]byte
}

// Has reports whether key holds a value.
func (s Secret) Has(key string) bool { return len(s.Data[key]) > 0 }

// String is key's value.
func (s Secret) String(key string) string { return string(s.Data[key]) }

// Secrets reads and patches the installation's Secrets.
type Secrets interface {
	// Get reads a Secret; one that doesn't exist is errSecretMissing.
	Get(ctx context.Context, name string) (Secret, error)
	// Patch sets keys in a Secret read at resourceVersion (a nil value
	// removes the key), and fails with errConflict when it changed since.
	Patch(ctx context.Context, name, resourceVersion string, data map[string][]byte) error
}

// KubeSecrets is Secrets through the Kubernetes API, in one namespace.
type KubeSecrets struct {
	Client    kubernetes.Interface
	Namespace string
}

// Get reads name.
func (k KubeSecrets) Get(ctx context.Context, name string) (Secret, error) {
	s, err := k.Client.CoreV1().Secrets(k.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Secret{Name: name}, fmt.Errorf("%w: %s", errSecretMissing, name)
	}
	if err != nil {
		return Secret{Name: name}, fmt.Errorf("get Secret %s: %w", name, err)
	}
	return Secret{Name: name, ResourceVersion: s.ResourceVersion, Data: s.Data}, nil
}

// Patch merges data into name, as of resourceVersion: a merge patch that
// names a resourceVersion is refused when the object has moved on. Only
// data changes, so kubectl apply's annotation and labels stay.
func (k KubeSecrets) Patch(ctx context.Context, name, resourceVersion string, data map[string][]byte) error {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": resourceVersion},
		"data":     data, // []byte is base64 in JSON; nil is null, which removes the key
	})
	if err != nil {
		return fmt.Errorf("patch Secret %s: %w", name, err)
	}
	_, err = k.Client.CoreV1().Secrets(k.Namespace).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "exemplar-setup"})
	switch {
	case apierrors.IsConflict(err):
		return fmt.Errorf("%w: %s", errConflict, name)
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%w: %s", errSecretMissing, name)
	case err != nil:
		return fmt.Errorf("patch Secret %s: %w", name, err)
	}
	return nil
}
