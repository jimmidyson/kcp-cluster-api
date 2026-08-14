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

	"sigs.k8s.io/cluster-api/internal/contract"
	"sigs.k8s.io/cluster-api/kcp/internal/contractmetadata"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// devInfraContractLabels mirrors the labels
// test/infrastructure/docker/config/crd/kustomization.yaml's labels
// transformer stamps onto its CRDs at build time (see
// kcp/internal/contractmetadata/contractmetadata_test.go, which asserts
// against the real file so this can't silently drift). Hardcoded rather
// than read from that file at runtime: a built binary doesn't carry the
// source tree with it.
var devInfraContractLabels = map[string]string{
	"cluster.x-k8s.io/v1beta1": "v1beta1",
	"cluster.x-k8s.io/v1beta2": "v1beta2",
}

// SetupContractMetadata installs a contractmetadata.Registry as
// internal/contract.GetGKMetadataFunc - the fork's one deliberate, tracked
// exception to the upstream-is-read-only invariant (see AGENTS.md and
// ADR-0001's "Known gaps" section) - covering the docker/dev infrastructure
// provider types this walking skeleton reconciles. GetGKMetadata is the
// single root every contract-version lookup in core/reconcilers and
// controllers/external funnels through, so this one override covers all of
// them (GetObjectFromContractVersionedRef, GetContractVersion, GetAPIVersion)
// uniformly. SetupContractMetadata is process-global (GetGKMetadataFunc is a
// package var), so call it once per process, before SetupReconcilers.
func SetupContractMetadata() {
	reg := contractmetadata.New()
	for _, kind := range []string{"DevCluster", "DevMachine", "DevClusterTemplate", "DevMachineTemplate"} {
		reg.Add(schema.GroupKind{Group: infrav1.GroupVersion.Group, Kind: kind}, devInfraContractLabels)
	}
	contract.GetGKMetadataFunc = reg.GetGKMetadata
}
