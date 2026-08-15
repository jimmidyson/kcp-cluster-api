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
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/cluster-api/controllers/external"
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

// SetupContractMetadata installs a contractmetadata.Registry through
// external.SetGKMetadataGetter, the public seam this project's Cluster API
// fork provides (see DRIFT.md), covering the docker/dev infrastructure
// provider types this skeleton reconciles.
//
// Contract-version lookups in core/reconcilers and controllers/external all
// funnel through a single resolver, so this one override covers all of them
// (GetObjectFromContractVersionedRef, GetContractVersion, GetAPIVersion)
// uniformly.
//
// It is process-global, so call it once per process, before SetupReconcilers.
func SetupContractMetadata() {
	reg := contractmetadata.New()
	for _, kind := range []string{"DevCluster", "DevMachine", "DevClusterTemplate", "DevMachineTemplate"} {
		reg.Add(schema.GroupKind{Group: infrav1.GroupVersion.Group, Kind: kind}, devInfraContractLabels)
	}
	external.SetGKMetadataGetter(reg.GetGKMetadata)
}
