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

package multicluster

import (
	"context"

	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// NewClusterAwareClient returns a client that resolves, per call, to the client
// of the cluster named in the call's context.
//
// # Why this exists
//
// It is what lets one controller serve many clusters without any reconciler
// changing. A reconciler holds a single client.Client field and calls it with
// the context it was given; the builder puts the cluster in that context, and
// this resolves it. Nothing on the reconcile path has to know there is more than
// one cluster.
//
// # Why it errors rather than falling back
//
// A call with no cluster in its context is a wiring mistake — a handler that was
// not lifted, a goroutine started with a bare context, a helper that dropped the
// one it was passed. Falling back to the local cluster would turn each of those
// into a silent cross-cluster read or write. It returns an error instead, on the
// call that made the mistake.
//
// # What is not context-scoped, and why that is safe
//
// Scheme, RESTMapper, GroupVersionKindFor and IsObjectNamespaced take no
// context, so they answer from the local manager. Scheme is process-wide, so
// that is exact. The other three go through the local RESTMapper, which is
// correct as long as every cluster serves the same API surface for the types
// Cluster API works with — they are resolving Cluster API's own kinds and the
// contract-versioned provider kinds, which a provider installs identically
// across the clusters it serves. A cluster that served a different set would
// need its own mapper, and that would need these methods to take a context.
func NewClusterAwareClient(mgr mcmanager.Manager) client.Client {
	return &clusterAwareClient{mgr: mgr, local: mgr.GetLocalManager().GetClient()}
}

// NewClusterAwareAPIReader is NewClusterAwareClient for the uncached reader that
// reconcilers hold alongside their client.
func NewClusterAwareAPIReader(mgr mcmanager.Manager) client.Reader {
	return &clusterAwareReader{resolve: func(ctx context.Context) (client.Reader, error) {
		cl, err := clusterFor(ctx, mgr)
		if err != nil {
			return nil, err
		}
		return cl.GetAPIReader(), nil
	}}
}

func clusterFor(ctx context.Context, mgr mcmanager.Manager) (interface {
	GetClient() client.Client
	GetAPIReader() client.Reader
}, error,
) {
	name, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return nil, pkgerrors.New("no cluster in context: a cluster-aware client was called with a context that was not scoped to a cluster")
	}
	cl, err := mgr.GetCluster(ctx, name)
	if err != nil {
		return nil, pkgerrors.Wrapf(err, "failed to get cluster %q", name)
	}
	return cl, nil
}

type clusterAwareReader struct {
	resolve func(context.Context) (client.Reader, error)
}

func (r *clusterAwareReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	return c.Get(ctx, key, obj, opts...)
}

func (r *clusterAwareReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	return c.List(ctx, list, opts...)
}

type clusterAwareClient struct {
	mgr mcmanager.Manager

	// local answers the four methods that take no context. See the note on
	// NewClusterAwareClient.
	local client.Client
}

var _ client.Client = &clusterAwareClient{}

func (c *clusterAwareClient) clientFor(ctx context.Context) (client.Client, error) {
	cl, err := clusterFor(ctx, c.mgr)
	if err != nil {
		return nil, err
	}
	return cl.GetClient(), nil
}

func (c *clusterAwareClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Get(ctx, key, obj, opts...)
}

func (c *clusterAwareClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.List(ctx, list, opts...)
}

func (c *clusterAwareClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Apply(ctx, obj, opts...)
}

func (c *clusterAwareClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Create(ctx, obj, opts...)
}

func (c *clusterAwareClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Delete(ctx, obj, opts...)
}

func (c *clusterAwareClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Update(ctx, obj, opts...)
}

func (c *clusterAwareClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Patch(ctx, obj, patch, opts...)
}

func (c *clusterAwareClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	cl, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.DeleteAllOf(ctx, obj, opts...)
}

// Status and SubResource take no context, so they cannot resolve a cluster.
// They return a sub-client that defers resolution to the call that does have
// one, which is every method on it.
func (c *clusterAwareClient) Status() client.SubResourceWriter {
	return c.SubResource("status")
}

func (c *clusterAwareClient) SubResource(subResource string) client.SubResourceClient {
	return &clusterAwareSubResourceClient{parent: c, subResource: subResource}
}

func (c *clusterAwareClient) Scheme() *runtime.Scheme     { return c.local.Scheme() }
func (c *clusterAwareClient) RESTMapper() meta.RESTMapper { return c.local.RESTMapper() }
func (c *clusterAwareClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.local.GroupVersionKindFor(obj)
}

func (c *clusterAwareClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return c.local.IsObjectNamespaced(obj)
}

type clusterAwareSubResourceClient struct {
	parent      *clusterAwareClient
	subResource string
}

var _ client.SubResourceClient = &clusterAwareSubResourceClient{}

func (s *clusterAwareSubResourceClient) clientFor(ctx context.Context) (client.SubResourceClient, error) {
	cl, err := s.parent.clientFor(ctx)
	if err != nil {
		return nil, err
	}
	return cl.SubResource(s.subResource), nil
}

func (s *clusterAwareSubResourceClient) Get(ctx context.Context, obj, subResource client.Object, opts ...client.SubResourceGetOption) error {
	cl, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Get(ctx, obj, subResource, opts...)
}

func (s *clusterAwareSubResourceClient) Create(ctx context.Context, obj, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	cl, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Create(ctx, obj, subResource, opts...)
}

func (s *clusterAwareSubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	cl, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Update(ctx, obj, opts...)
}

func (s *clusterAwareSubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	cl, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Patch(ctx, obj, patch, opts...)
}

func (s *clusterAwareSubResourceClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	cl, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	return cl.Apply(ctx, obj, opts...)
}
