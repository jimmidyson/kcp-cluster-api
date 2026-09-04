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

package deployedscale

import (
	"context"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func storageClass(name string, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if isDefault {
		sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return sc
}

func storageClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fakeClient(objects...)
}

// TestAClusterWithNoProvisionerIsCaughtBeforeTheStoreIsApplied.
//
// The stock side's cluster is generated with the CSI addon trimmed — nothing in
// that measurement asks for a volume, and every addon left on is another
// controller reconciling against the API server whose cost is the subject. The
// kcp side asks for three volumes, one per etcd member.
//
// Without this check that mismatch surfaces as three Pending pods, then kcp
// failing to come up because its store never answered, then a timeout naming
// the shard — three sentences away from "this cluster has no storage class".
func TestAClusterWithNoProvisionerIsCaughtBeforeTheStoreIsApplied(t *testing.T) {
	err := StorageAvailable(context.Background(), storageClient(t), "")
	if err == nil {
		t.Fatal("a cluster with no storage class was reported as ready for a store")
	}
	if !strings.Contains(err.Error(), "storage class") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// TestANamedClassThatIsNotThereIsNamedInTheError, rather than reported as
// "no storage class" when the cluster has several.
func TestANamedClassThatIsNotThereIsNamedInTheError(t *testing.T) {
	cl := storageClient(t, storageClass("nutanix-volume", true))

	if err := StorageAvailable(context.Background(), cl, "nutanix-volume"); err != nil {
		t.Errorf("the class that is there was refused: %v", err)
	}

	err := StorageAvailable(context.Background(), cl, "does-not-exist")
	if err == nil {
		t.Fatal("a class that is not there was accepted")
	}
	if !strings.Contains(err.Error(), "does-not-exist") || !strings.Contains(err.Error(), "nutanix-volume") {
		t.Errorf("the error names neither what was asked for nor what there is: %v", err)
	}
}

// TestNoClassNamedTakesTheDefault, and refuses when there is not one: a
// PersistentVolumeClaim with no class and no default is Pending forever.
func TestNoClassNamedTakesTheDefault(t *testing.T) {
	withDefault := storageClient(t, storageClass("nutanix-volume", true))
	if err := StorageAvailable(context.Background(), withDefault, ""); err != nil {
		t.Errorf("a cluster with a default class was refused: %v", err)
	}

	withoutDefault := storageClient(t, storageClass("nutanix-volume", false))
	err := StorageAvailable(context.Background(), withoutDefault, "")
	if err == nil {
		t.Fatal("a cluster whose classes are all non-default was accepted with no class named")
	}
	if !strings.Contains(err.Error(), "nutanix-volume") {
		t.Errorf("the error does not say what could be named instead: %v", err)
	}
}
