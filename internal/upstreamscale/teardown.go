/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package upstreamscale

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Teardown removes what a run created: every Cluster in the namespaces, then,
// once none remain, the namespaces themselves.
//
// # Why not just the namespaces
//
// The first run deleted its namespaces and left every one of them Terminating,
// with the DevCluster provider logging that it could not connect to a cluster
// whose kubeconfig Secret was gone. The Secret was a symptom. Deleting a
// namespace stamps every object in it at once, and stock Cluster API cannot
// finish from there: a deleting DevCluster removes its finalizer immediately,
// taking the in-memory state every DevMachine would clean up with it, and a
// deleting DevMachine whose DevCluster has gone logs "DevCluster is not
// available yet" and waits for it forever. Its Machine waits for it, the
// Cluster waits for its Machines, and the namespace waits for the Cluster.
//
// Deleting the Cluster instead lets the Cluster controller order its
// descendants the way upstream keeps them: workers, then the control plane,
// then the infrastructure cluster, and only then the Cluster and the Secrets
// it owns. This repository's fork carries fixes for both halves of the
// out-of-order case (see DRIFT.md), because deleting a kcp APIBinding removes
// everything at once exactly as a namespace does — but the cluster under test
// runs stock Cluster API on purpose, so the harness keeps the order itself.
//
// # Why it waits
//
// A namespace deleted over a Cluster still in it is the failure above, so
// none is deleted until no Cluster remains. A wait that runs out reports what
// it was still waiting for, by name and with Cluster API's own account of
// why, and leaves the namespace alone: a stuck fleet an operator can still
// read is better than one that has been stamped a second time.
//
// Safe against a previous run that died: a namespace that is not there is not
// an error, and a Cluster already being deleted is deleted again harmlessly.
func Teardown(ctx context.Context, cl client.Client, namespaces []string, timeout, poll time.Duration,
	logf func(format string, args ...any),
) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	namespaces = distinct(namespaces)

	deleted, err := DeleteClusters(ctx, cl, namespaces)
	if err != nil {
		return err
	}
	logf("teardown: asked for %d Clusters in %d namespaces to be deleted", deleted, len(namespaces))
	if err := WaitForClustersGone(ctx, cl, namespaces, timeout, poll); err != nil {
		return err
	}

	for _, ns := range namespaces {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := cl.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting namespace %s: %w", ns, err)
		}
	}
	if err := WaitForNamespacesGone(ctx, cl, namespaces, timeout, poll); err != nil {
		return err
	}
	logf("teardown: %d namespaces gone", len(namespaces))
	return nil
}

// DeleteClusters asks for every Cluster in the namespaces to be deleted, and
// says how many that was. A namespace that does not exist holds no Clusters.
func DeleteClusters(ctx context.Context, cl client.Client, namespaces []string) (int, error) {
	deleted := 0
	for _, ns := range namespaces {
		var clusters clusterv1.ClusterList
		if err := cl.List(ctx, &clusters, client.InNamespace(ns)); err != nil {
			return deleted, fmt.Errorf("listing Clusters in %s: %w", ns, err)
		}
		for i := range clusters.Items {
			c := &clusters.Items[i]
			if err := cl.Delete(ctx, c); err != nil && !apierrors.IsNotFound(err) {
				return deleted, fmt.Errorf("deleting Cluster %s/%s: %w", c.Namespace, c.Name, err)
			}
			deleted++
		}
	}
	return deleted, nil
}

// WaitForClustersGone polls until no Cluster remains in any of the namespaces.
//
// On timeout the error names what remains — up to a few by name, each with
// the message of its Deleting condition, which is the Cluster controller's own
// statement of what it is waiting for.
func WaitForClustersGone(ctx context.Context, cl client.Client, namespaces []string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var remaining []string
		for _, ns := range namespaces {
			var clusters clusterv1.ClusterList
			if err := cl.List(ctx, &clusters, client.InNamespace(ns)); err != nil {
				return fmt.Errorf("listing Clusters in %s: %w", ns, err)
			}
			for i := range clusters.Items {
				remaining = append(remaining, describeRemaining(&clusters.Items[i]))
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d Clusters still present after %s, so their namespaces were left alone: %s",
				len(remaining), timeout, firstFew(remaining, 5))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// WaitForNamespacesGone polls until none of the namespaces exist.
//
// Not tidiness: a rerun that started while the previous fleet's namespaces
// were still terminating would take its baseline against a cluster still
// deleting, and nothing in the report would say so.
func WaitForNamespacesGone(ctx context.Context, cl client.Client, namespaces []string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var remaining []string
		for _, ns := range namespaces {
			err := cl.Get(ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{})
			switch {
			case apierrors.IsNotFound(err):
			case err != nil:
				return fmt.Errorf("waiting for namespace %s to go: %w", ns, err)
			default:
				remaining = append(remaining, ns)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d namespaces still terminating after %s: %s",
				len(remaining), timeout, firstFew(remaining, 5))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func describeRemaining(c *clusterv1.Cluster) string {
	name := c.Namespace + "/" + c.Name
	for _, cond := range c.Status.Conditions {
		if cond.Type == clusterv1.ClusterDeletingCondition && cond.Message != "" {
			return name + " (" + cond.Message + ")"
		}
	}
	if c.DeletionTimestamp.IsZero() {
		return name + " (not being deleted)"
	}
	return name
}

func firstFew(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, "; ")
	}
	return strings.Join(items[:n], "; ") + fmt.Sprintf("; and %d more", len(items)-n)
}

func distinct(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
