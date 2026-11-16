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

package clustercache

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// clusterGoneReader is a client.Reader that answers the way a cluster-aware
// client does once the provider has stopped serving a workspace.
type clusterGoneReader struct {
	client.Client
	calls int
}

func (r *clusterGoneReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	r.calls++
	return pkgerrors.Wrapf(multicluster.ErrClusterNotFound, "failed to get cluster %q", "gone")
}

// TestReconcileStopsWhenTheLogicalClusterIsGone covers what an unbinding
// workspace costs after it has gone.
//
// A fleet-wide ClusterCache reconciles Clusters it learned about through a
// cache that spans every workspace, so a workspace that unbinds leaves its
// Clusters in the queue. Treating "the workspace is no longer served" as a
// transient read error requeues each of them every ten seconds for the life of
// the process, which is a leak that grows with tenant churn and that no
// measurement finds unless it unbinds something.
//
// Asserted through Reconcile's result rather than by counting log lines,
// because what matters is that nothing is scheduled: a requeue here is the
// defect, whatever it says while doing it.
func TestReconcileStopsWhenTheLogicalClusterIsGone(t *testing.T) {
	g := NewWithT(t)

	reader := &clusterGoneReader{}
	cacheCtx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })

	cc := &clusterCache{
		client:                reader,
		clusterAccessorConfig: buildClusterAccessorConfig(runtime.NewScheme(), Options{SecretClient: reader}, nil),
		clusterAccessors:      make(map[accessorKey]*clusterAccessor),
		cacheCtx:              cacheCtx,
		cacheCtxCancel:        cancel,
	}

	ctx := mccontext.WithCluster(context.Background(), "gone")
	req := mcreconcile.Request{ClusterName: "gone"}
	req.Namespace = "default"
	req.Name = "workload"

	// An accessor exists, because the workspace was served until a moment ago.
	key := keyFor(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name})
	g.Expect(cc.getOrCreateClusterAccessor(key)).ToNot(BeNil())

	res, err := cc.Reconcile(ctx, req.Request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(res.RequeueAfter).To(BeZero(), "a workspace that is gone must stop being polled")
	g.Expect(res.Requeue).To(BeFalse()) //nolint:staticcheck // mirrors the field Reconcile still sets.

	// And the accessor is released rather than left holding a connection to a
	// workload cluster nothing is managing any more.
	g.Expect(cc.getClusterAccessor(key)).To(BeNil())
	g.Expect(reader.calls).To(Equal(1))
}
