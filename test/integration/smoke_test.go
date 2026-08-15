//go:build integration

package integration

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

// TestKCPServerBecomesReady is a canary for the KCP envtest harness itself:
// it starts a real kcp server, targets its root workspace, and confirms a
// plain client-go client can list resources through it. New integration
// suites belong in their own package alongside the controller or client
// code they exercise; this test only proves the harness works.
func TestKCPServerBecomesReady(t *testing.T) {
	cfg := envtest.Environment(t, "")

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	if _, err := client.CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{}); err != nil {
		t.Fatalf("failed to list namespaces in the root workspace: %v", err)
	}
}
