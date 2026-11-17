package client

import (
	v4Converged "github.com/nutanix-cloud-native/prism-go-client/converged/v4"
	"github.com/nutanix-cloud-native/prism-go-client/environment/types"
	v3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	v4 "github.com/nutanix-cloud-native/prism-go-client/v4"

	"github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
)

// NutanixClientCache is the cache of prism clients to be shared across the different controllers
var NutanixClientCache = v3.NewClientCache(v3.WithSessionAuth(true))

// NutanixClientCacheV4 is the cache of prism clients to be shared across the different controllers
var NutanixClientCacheV4 = v4.NewClientCache(v4.WithSessionAuth(true))

// NutanixConvergedClientV4Cache is the cache of prism clients to be shared across the different controllers
var NutanixConvergedClientV4Cache = v4Converged.NewClientCache(v4.WithSessionAuth(true))

// CacheParams is the struct that implements ClientCacheParams interface from prism-go-client
type CacheParams struct {
	NutanixCluster          *v1beta1.NutanixCluster
	PrismManagementEndpoint *types.ManagementEndpoint
}

// Key identifies the cached Prism client for a NutanixCluster.
//
// # Why the namespace and name are not enough
//
// The caches this key indexes are process-global and hold session-authenticated
// clients. A namespace and name are unique within one API server, which is all
// Cluster API normally has. Under kcp a workspace is a whole API server, two
// workspaces routinely hold a NutanixCluster of the same namespace and name,
// and one process serves both.
//
// Unqualified, the second workspace to reconcile is handed the first's client:
// its clusters are then created on the first tenant's Prism Central, with the
// first tenant's credentials. Nothing fails — the wrong tenant's session simply
// works — so the qualification is the only thing standing between the two.
//
// A NutanixCluster read outside kcp carries no logical cluster and gets exactly
// the key it always had.
func (c *CacheParams) Key() string {
	if c.NutanixCluster == nil {
		return ""
	}
	name := c.NutanixCluster.GetNamespacedName()
	if lc := v1beta1.LogicalClusterFrom(c.NutanixCluster.GetAnnotations()); lc != "" {
		return lc + "/" + name
	}
	return name
}

// ManagementEndpoint returns the management endpoint of the NutanixCluster CR
func (c *CacheParams) ManagementEndpoint() types.ManagementEndpoint {
	return *c.PrismManagementEndpoint
}
