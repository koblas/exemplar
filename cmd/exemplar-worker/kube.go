package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/koblas/exemplar/internal/worker/kubelaunch"
)

// namespaceFile is the pod's namespace in its service account projection.
const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// kubeLauncher is the Kubernetes Job launcher, from the pod's in-cluster
// service account (mounted on control only).
func kubeLauncher(ctx context.Context, template string, log *slog.Logger) (*kubelaunch.Launcher, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes launcher: in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("kubernetes launcher: client: %w", err)
	}
	ns, err := os.ReadFile(namespaceFile)
	if err != nil {
		return nil, fmt.Errorf("kubernetes launcher: namespace: %w", err)
	}
	l, err := kubelaunch.New(ctx, kubelaunch.Config{
		Client: client, Namespace: strings.TrimSpace(string(ns)), Template: template, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("kubernetes launcher: %w", err)
	}
	return l, nil
}
