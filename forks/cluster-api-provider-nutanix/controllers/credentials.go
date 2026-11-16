/*
Copyright 2026 Nutanix

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

package controllers

import (
	coreinformers "k8s.io/client-go/informers/core/v1"
	ctlclient "sigs.k8s.io/controller-runtime/pkg/client"

	nutanixclient "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/pkg/client"
)

// credentialSource is where a reconciler reads a NutanixCluster's Prism
// Central credentials from.
//
// There are two, and which one is in use is the difference between a
// single-cluster CAPX and a fleet-wide one:
//
//   - Reader is a cluster-aware client. Each read resolves against the cluster
//     named in the context it is given, which is the only way to tell two
//     workspaces' identically-named credentials Secrets apart.
//   - The informers are the single-cluster path, unchanged. They are built over
//     one clientset and address one API server, which is all a CAPX running
//     against an ordinary management cluster has.
//
// Reader wins when set. It is a struct rather than a fifth and sixth argument
// because handing the wrong one to a fleet-wide reconciler does not fail — it
// provisions one tenant's cluster with another tenant's credentials — and that
// is worth naming rather than leaving as an argument order to get right.
type credentialSource struct {
	Reader ctlclient.Reader

	SecretInformer    coreinformers.SecretInformer
	ConfigMapInformer coreinformers.ConfigMapInformer
}

func (c credentialSource) helper() *nutanixclient.NutanixClientHelper {
	if c.Reader != nil {
		return nutanixclient.NewWorkspaceHelper(c.Reader)
	}
	return nutanixclient.NewHelper(c.SecretInformer, c.ConfigMapInformer)
}
