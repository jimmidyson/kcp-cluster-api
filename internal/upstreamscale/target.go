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

package upstreamscale

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Target is one side of the comparison: stock Cluster API on a Kubernetes API
// server, or this project's on a kcp shard.
//
// # Why an interface rather than two drivers
//
// The two sides were measured by two harnesses, and most of what could be said
// about the resulting figures was why they could not quite be subtracted. A
// difference in the numbers has to be a difference in the system, so the
// ladder, the settle, the sampling, the defragmentation, the soak, the drift
// check, the teardown and the report are one piece of code and this is
// everything the two sides are allowed to differ in.
//
// # The two clients
//
// A run addresses two things and they are not the same cluster. The **fleet**
// lives wherever Clusters are created — the hosting cluster's own API server on
// the stock side, a kcp workspace on the other — and a Target owns that. The
// **processes** being measured are pods on the hosting cluster either way, and
// the Runner owns that, because reading a pod's pprof is identical whichever
// side put the pod there.
//
// Everything on this interface is therefore about the fleet or about naming
// what to sample. Nothing on it is about how to sample.
type Target interface {
	// Name is what the report calls this side, and what tells two report files
	// apart.
	Name() string

	// Title is the report's heading.
	Title(startClusters, nodesPerCluster int) string

	// Facts are what this side says about itself: its Cluster API, its
	// tenancy unit, its shape. The Runner adds the ones that are true of both.
	Facts() map[string]string

	// Prepare checks the target can serve what the run is about to create,
	// before anything is created. On the stock side this is the one risk no
	// unit test can find: the objects come from this repository's fork and the
	// CRDs from whatever clusterctl installed.
	Prepare(ctx context.Context) error

	// Controllers is what this side runs, for the sampler to read. Four
	// managers either way, in different namespaces under different names.
	Controllers() []Controller

	// ControlPlane samples the process a fleet's objects live in — the API
	// server on one side, the shard replicas on the other — as samples to add
	// to the report and one line describing them.
	//
	// Every replica, not one: the stock side runs three API servers behind a
	// VIP and the kcp side runs three shard replicas, so a figure from one
	// process is a third of a control plane and says so. See
	// Sampler.ControlPlanes.
	//
	// host is the cluster those processes are pods on, which is the Runner's
	// client rather than the Target's: this is the one place a Target needs the
	// hosting side, because only it knows where its own control plane lives and
	// only the Runner knows how to reach a pod.
	ControlPlane(ctx context.Context, host client.Client, heapSamples int, heapGap time.Duration,
	) ([]deployedscale.ComponentSample, string, error)

	// Store is where this side's etcd is, for sampling and defragmenting.
	Store() StoreLocation

	// Plan resolves a rung's cluster count into the tenants and clusters to
	// create. A tenant is a Namespace on one side and a Workspace on the
	// other, which is the only thing about the fleet's shape that differs.
	Plan(clusters int) (Fleet, error)

	// Create applies a rung. It returns the tenants it created **even when it
	// fails**, so that a half-built rung is torn down rather than left.
	Create(ctx context.Context, fleet Fleet, concurrency int) ([]string, error)

	// Converged counts the fleet against the end state both sides wait for:
	// every control plane ready and every Machine Ready.
	Converged(ctx context.Context, wantClusters, wantMachines int) (Convergence, error)

	// Teardown removes what Create made, and is safe against a target where a
	// previous run died.
	Teardown(ctx context.Context, created []string, timeout, poll time.Duration,
		logf func(string, ...any)) error
}

// RunOptions are the knobs a run takes, and they are the same knobs on both
// sides — which is most of what makes two runs comparable.
type RunOptions struct {
	// StartClusters and MaxClusters bound the doubling ladder.
	StartClusters, MaxClusters int
	// NodesPerCluster is carried for the report; the shape itself is the
	// Target's, since it knows what a tenant is.
	NodesPerCluster int

	// CreateConcurrency is how many tenants are created at once. Recorded
	// rather than assumed: the driver's own throughput is not the subject and
	// was once most of a rung's wall time.
	CreateConcurrency int

	// SettleTolerance and SettleTimeout wait for the controllers to finish
	// starting before the baseline. See Settled.
	SettleTolerance float64
	SettleTimeout   time.Duration

	// StepTimeout is how long one rung may take to converge, PollInterval how
	// often it is checked.
	StepTimeout, PollInterval time.Duration

	// Soak and SoakInterval hold the largest fleet that converged.
	Soak, SoakInterval time.Duration

	// TeardownTimeout bounds the wait for the fleet to go.
	TeardownTimeout time.Duration

	// APIHeapSamples and APIHeapGap are the control plane's heap floor. See
	// LowestHeap.
	APIHeapSamples int
	APIHeapGap     time.Duration

	// DriverFact describes the driver's own limits, which belong in the report
	// because they bound what its timings mean.
	DriverFact string
}
