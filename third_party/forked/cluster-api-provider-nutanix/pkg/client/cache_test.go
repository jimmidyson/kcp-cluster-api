package client

import (
	"net/url"
	"testing"

	v4Converged "github.com/nutanix-cloud-native/prism-go-client/converged/v4"
	"github.com/nutanix-cloud-native/prism-go-client/environment/types"
	v3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	v4 "github.com/nutanix-cloud-native/prism-go-client/v4"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
)

func TestCacheParamsKey(t *testing.T) {
	cluster := &v1beta1.NutanixCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
	}

	params := CacheParams{
		NutanixCluster: cluster,
	}

	expectedKey := "test-namespace/test-cluster"
	assert.Equal(t, expectedKey, params.Key())
}

func TestCacheParamsManagementEndpoint(t *testing.T) {
	endpoint := &types.ManagementEndpoint{
		Address: &url.URL{
			Scheme: "https",
			Host:   "prismcentral.nutanix.com:9440",
		},
	}

	params := &CacheParams{
		PrismManagementEndpoint: endpoint,
	}

	assert.Equal(t, *endpoint, params.ManagementEndpoint())
}

func TestNutanixClientCache(t *testing.T) {
	assert.NotNil(t, NutanixClientCache)
	assert.IsType(t, &v3.ClientCache{}, NutanixClientCache)
}

func TestNutanixClientCacheV4(t *testing.T) {
	assert.NotNil(t, NutanixClientCacheV4)
	assert.IsType(t, &v4.ClientCache{}, NutanixClientCacheV4)
}

func TestNutanixConvergedClientV4Cache(t *testing.T) {
	assert.NotNil(t, NutanixConvergedClientV4Cache)
	assert.IsType(t, &v4Converged.ClientCache{}, NutanixConvergedClientV4Cache)
}

// TestCacheParamsKeySeparatesWorkspaces is the breach this key exists to stop.
//
// The caches it indexes are process-global and hold session-authenticated
// clients, so two workspaces sharing a key do not merely confuse two clusters:
// the second tenant is handed the first tenant's authenticated session and
// creates its VMs on the first tenant's Prism Central. That succeeds, which is
// what makes it worth a test rather than a comment.
func TestCacheParamsKeySeparatesWorkspaces(t *testing.T) {
	inWorkspace := func(logicalCluster string) *CacheParams {
		return &CacheParams{NutanixCluster: &v1beta1.NutanixCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "demo",
				Namespace:   "default",
				Annotations: map[string]string{v1beta1.LogicalClusterAnnotation: logicalCluster},
			},
		}}
	}

	alpha := inWorkspace("workspace-alpha")
	beta := inWorkspace("workspace-beta")

	assert.NotEqual(t, alpha.Key(), beta.Key(),
		"two workspaces holding a same-named NutanixCluster must not share a Prism client")
	assert.Equal(t, "workspace-alpha/default/demo", alpha.Key())
	assert.Equal(t, "workspace-beta/default/demo", beta.Key())
}

// TestCacheParamsKeyOutsideKCP pins the property that keeps an installation
// without kcp on exactly the key it had before.
func TestCacheParamsKeyOutsideKCP(t *testing.T) {
	params := &CacheParams{NutanixCluster: &v1beta1.NutanixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}}

	assert.Equal(t, "default/demo", params.Key())
}

// TestCacheParamsKeyWithoutACluster covers the nil case rather than panicking
// on it, which is the shape of bug this provider has had before.
func TestCacheParamsKeyWithoutACluster(t *testing.T) {
	assert.Equal(t, "", (&CacheParams{}).Key())
}
