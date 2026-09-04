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
	"errors"
	"fmt"
	"sort"
	"strings"

	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultStorageClassAnnotation is how a cluster marks the class a claim with
// no class named gets.
const defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// StorageAvailable checks the cluster can provision the store's volumes, before
// anything is applied.
//
// # Why this is worth a check of its own
//
// The two sides of the comparison share one cluster, and that cluster is
// generated for the stock side — where the CSI addon is trimmed, because
// nothing in that measurement asks for a volume and every addon left on is
// another controller reconciling against the API server whose cost is the
// subject. The kcp side asks for three volumes, one per etcd member, because a
// member that loses its data directory cannot rejoin the quorum.
//
// Without this the mismatch arrives as three Pending pods, then a shard that
// never comes up because its store never answered, then a timeout naming the
// shard — three sentences away from the one that is true.
func StorageAvailable(ctx context.Context, cl client.Client, named string) error {
	var classes storagev1.StorageClassList
	if err := cl.List(ctx, &classes); err != nil {
		return fmt.Errorf("listing storage classes: %w", err)
	}

	names := make([]string, 0, len(classes.Items))
	var defaults []string
	for i := range classes.Items {
		sc := &classes.Items[i]
		names = append(names, sc.Name)
		if sc.Annotations[defaultStorageClassAnnotation] == "true" {
			defaults = append(defaults, sc.Name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		return errors.New("this cluster has no storage class at all, so every etcd member would wait " +
			"forever for a volume nothing can provision. The cluster the stock side runs on is generated " +
			"with the CSI addon trimmed unless it is asked for (KEEP_CSI); install a provisioner, or " +
			"regenerate the cluster with one")
	}

	if named == "" {
		if len(defaults) == 0 {
			return fmt.Errorf("no storage class was named and this cluster has no default one, so a claim "+
				"with no class would stay Pending. Name one of: %s", strings.Join(names, ", "))
		}
		return nil
	}

	for _, name := range names {
		if name == named {
			return nil
		}
	}
	return fmt.Errorf("no storage class %q on this cluster; it has: %s", named, strings.Join(names, ", "))
}
