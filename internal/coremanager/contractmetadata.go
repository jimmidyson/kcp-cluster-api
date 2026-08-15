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

package coremanager

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/core/webhooks/conversion"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/contractmetadata"
)

// devInfraContractLabels mirrors the labels
// test/infrastructure/docker/config/crd/kustomization.yaml's labels
// transformer stamps onto its CRDs at build time (see
// internal/contractmetadata/contractmetadata_test.go, which asserts
// against the real file so this can't silently drift). Hardcoded rather
// than read from that file at runtime: a built binary doesn't carry the
// source tree with it.
var devInfraContractLabels = map[string]string{
	"cluster.x-k8s.io/v1beta1": "v1beta1",
	"cluster.x-k8s.io/v1beta2": "v1beta2",
}

// SetupProcessGlobals installs the two resolvers upstream requires to be set
// process-wide, both backed by a contractmetadata.Registry covering the
// docker/dev infrastructure provider types this skeleton reconciles:
//
//   - external.SetGKMetadataGetter, the public seam this project's Cluster API
//     fork provides (see DRIFT.md), used by every contract-versioned reference
//     resolution in core/reconcilers and controllers/external.
//   - conversion.SetAPIVersionGetter, the equivalent seam upstream already had
//     for the conversion webhook's own call path.
//
// Contract-version lookups funnel through a single resolver, so the first
// override covers all of them (GetObjectFromContractVersionedRef,
// GetContractVersion, GetAPIVersion) uniformly.
//
// Neither resolver may close over a workspace's client. There is one slot for
// each and many workspaces, so a workspace-scoped client stored here would be
// overwritten by the next workspace to start, and would then answer every
// other workspace's lookups — silently, since nothing about the last writer
// winning is an error. The registry avoids the question entirely: it is built
// from the CRD manifests of the pinned Cluster API modules, so it is a
// function of the build and is identical for every workspace.
//
// It is process-global, so call it once per process, before any workspace is
// set up.
func SetupProcessGlobals() {
	reg := contractmetadata.New()
	for _, kind := range []string{"DevCluster", "DevMachine", "DevClusterTemplate", "DevMachineTemplate"} {
		reg.Add(schema.GroupKind{Group: infrav1.GroupVersion.Group, Kind: kind}, devInfraContractLabels)
	}
	external.SetGKMetadataGetter(reg.GetGKMetadata)

	conversion.SetAPIVersionGetter(func(ctx context.Context, gk schema.GroupKind) (string, error) {
		return external.GetAPIVersion(ctx, noReader{}, gk)
	})
}

// noReader stands in for the client.Reader that upstream's signatures require
// and that this project has nothing workspace-independent to supply.
//
// The registry installed above resolves every lookup without reading from a
// cluster, so the reader is never consulted. Passing a typed error instead of
// nil is the difference between a clear message and a nil-pointer panic if the
// registry is ever removed or fails to cover a type.
type noReader struct{ client.Reader }

var errNoProcessWideClient = errors.New(
	"contract metadata must resolve from the static registry: no process-wide client exists, " +
		"because a client belongs to exactly one workspace")

func (noReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errNoProcessWideClient
}

func (noReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errNoProcessWideClient
}
