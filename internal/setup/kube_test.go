package setup

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKubeSecrets(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := fake.NewClientset(&corev1.Secret{
		Name: secretApp, Namespace: "exemplar", ResourceVersion: "1",
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
		Data:        map[string][]byte{"stale": []byte("x")},
	})
	k := KubeSecrets{Client: client, Namespace: "exemplar"}

	s, err := k.Get(ctx, secretApp)
	if err != nil || s.ResourceVersion != "1" || s.String("stale") != "x" {
		t.Fatalf("Get = %+v, %v", s, err)
	}
	if err := k.Patch(ctx, secretApp, s.ResourceVersion, map[string][]byte{"app-id": []byte("4242"), "stale": nil}); err != nil {
		t.Fatal(err)
	}
	got, err := client.CoreV1().Secrets("exemplar").Get(ctx, secretApp, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data["app-id"]) != "4242" || got.Data["stale"] != nil {
		t.Errorf("data after patch = %v, want app-id set and stale removed", got.Data)
	}
	if got.Annotations["kubectl.kubernetes.io/last-applied-configuration"] != "{}" {
		t.Error("patch dropped kubectl apply's annotation")
	}

	if _, err := k.Get(ctx, secretProviders); !errors.Is(err, errSecretMissing) {
		t.Errorf("Get of a missing Secret = %v, want errSecretMissing", err)
	}
	if err := k.Patch(ctx, secretProviders, "", map[string][]byte{"k": []byte("v")}); !errors.Is(err, errSecretMissing) {
		t.Errorf("Patch of a missing Secret = %v, want errSecretMissing", err)
	}

	// The fake doesn't check resourceVersion: the API server answers 409.
	client.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, secretApp, errors.New("changed"))
	})
	if err := k.Patch(ctx, secretApp, "1", map[string][]byte{"app-id": []byte("1")}); !errors.Is(err, errConflict) {
		t.Errorf("Patch on a conflict = %v, want errConflict", err)
	}
}
