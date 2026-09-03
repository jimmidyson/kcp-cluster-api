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
	"strings"
	"testing"
)

// A trimmed etcd exposition. The quota gauge is the one that matters most: the
// database size means nothing without the ceiling it is approaching.
const etcdBody = `# HELP etcd_mvcc_db_total_size_in_bytes Total size of the underlying database physically allocated in bytes.
etcd_mvcc_db_total_size_in_bytes 3.221225472e+09
etcd_mvcc_db_total_size_in_use_in_bytes 2.147483648e+09
etcd_debugging_mvcc_keys_total 412000
etcd_server_quota_backend_bytes 8.589934592e+09
etcd_server_has_leader 1
etcd_server_leader_changes_seen_total 2
etcd_server_slow_apply_total 1841
etcd_server_slow_read_indexes_total 7
etcd_disk_wal_fsync_duration_seconds_sum 184.5
etcd_disk_wal_fsync_duration_seconds_count 41000
etcd_disk_backend_commit_duration_seconds_sum 92.25
etcd_disk_backend_commit_duration_seconds_count 12300
`

// TestEtcdIsMeasuredAgainstItsCeiling. A database size is not a finding; a
// database size against the quota it is walking towards is. The kcp runs found
// the store, not the controllers, was what ran out, and this run expects the
// same — so the figure that says how close it is comes first.
func TestEtcdIsMeasuredAgainstItsCeiling(t *testing.T) {
	got, err := ParseEtcd(strings.NewReader(etcdBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.DBTotalBytes != 3221225472 || got.QuotaBytes != 8589934592 {
		t.Fatalf("sizes = %+v", got)
	}
	if used := got.QuotaUsed(); used < 0.37 || used > 0.38 {
		t.Errorf("quota used = %v, want about 0.375", used)
	}
	if got.Keys != 412000 {
		t.Errorf("keys = %d", got.Keys)
	}

	// The two latencies that say the disk is the limit rather than the fleet.
	// Means rather than percentiles: a histogram's buckets are not in this
	// parser, and a mean that moves by an order of magnitude is the signal.
	if ms := got.WALFsyncMeanMillis(); ms < 4.4 || ms > 4.6 {
		t.Errorf("wal fsync mean = %v ms, want about 4.5", ms)
	}
	if ms := got.BackendCommitMeanMillis(); ms < 7.4 || ms > 7.6 {
		t.Errorf("backend commit mean = %v ms, want about 7.5", ms)
	}

	if !got.HasLeader || got.LeaderChanges != 2 || got.SlowApplies != 1841 {
		t.Errorf("health = %+v", got)
	}

	// A quota this close is the headline of any rung that fails.
	near := got
	near.DBTotalBytes = 8_000_000_000
	if !near.NearQuota() {
		t.Error("93% of the backend quota was not called near it")
	}
	if got.NearQuota() {
		t.Error("37% of the quota was called near it")
	}
}

func TestAnEtcdBodyWithoutTheGaugesIsAnError(t *testing.T) {
	if _, err := ParseEtcd(strings.NewReader("# nothing\n")); err == nil {
		t.Error("an exposition with no etcd gauges parsed into a measurement of an empty store")
	}
}

// A trimmed kube-apiserver exposition.
const apiserverBody = `go_goroutines 4210
go_memstats_heap_alloc_bytes 6.1e+09
go_memstats_sys_bytes 9.2e+09
process_resident_memory_bytes 8.4e+09
process_cpu_seconds_total 5121.5
apiserver_current_inflight_requests{request_kind="mutating"} 12
apiserver_current_inflight_requests{request_kind="readOnly"} 47
apiserver_flowcontrol_rejected_requests_total{flow_schema="global-default",priority_level="global-default",reason="queue-full"} 318
etcd_request_duration_seconds_sum{operation="update",type="machines.cluster.x-k8s.io"} 410.0
etcd_request_duration_seconds_count{operation="update",type="machines.cluster.x-k8s.io"} 20000
apiserver_storage_objects{resource="machines.cluster.x-k8s.io"} 5000
apiserver_storage_objects{resource="events"} 18000
`

// TestTheAPIServerIsMeasuredForBothPressures: what it costs, and whether it is
// shedding load. A rung that fails while the API server is rejecting requests
// failed for a reason no controller's numbers would show.
func TestTheAPIServerIsMeasuredForBothPressures(t *testing.T) {
	got, err := ParseAPIServer(strings.NewReader(apiserverBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.Process.Goroutines != 4210 || got.Process.HeapAllocBytes != 6_100_000_000 {
		t.Errorf("process = %+v", got.Process)
	}
	if got.Process.ResidentBytes != 8_400_000_000 {
		t.Errorf("resident = %d", got.Process.ResidentBytes)
	}
	// Both kinds summed: an inflight count split by kind is two halves of one
	// pressure.
	if got.InflightRequests != 59 {
		t.Errorf("inflight = %d, want 59", got.InflightRequests)
	}
	if got.RejectedRequests != 318 {
		t.Errorf("rejected = %d, want 318", got.RejectedRequests)
	}
	if !got.SheddingLoad() {
		t.Error("an API server that has rejected requests was not reported as shedding load")
	}
	// The apiserver's own view of how slow etcd is being, which is the
	// clearest single number for "the store is the limit".
	if ms := got.EtcdRequestMeanMillis(); ms < 20.4 || ms > 20.6 {
		t.Errorf("etcd request mean = %v ms, want about 20.5", ms)
	}
	if got.StorageObjects != 23000 {
		t.Errorf("stored objects = %d, want 23000", got.StorageObjects)
	}
}

// TestStoredObjectsSeparateClusterApiFromEvents. S3 asks what the API server
// costs per stored Cluster API object, and the total cannot answer it: in this
// fixture, as in the first real runs, Events outnumber Cluster API objects
// several to one. They also expire on their own hour-long TTL, so the total
// moved by 2x between two runs of the same fleet size with nothing else
// changed — a denominator that drifts under a figure nobody would suspect.
func TestStoredObjectsSeparateClusterApiFromEvents(t *testing.T) {
	got, err := ParseAPIServer(strings.NewReader(apiserverBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.ClusterAPIObjects != 5000 {
		t.Errorf("Cluster API objects = %d, want 5000", got.ClusterAPIObjects)
	}
	if got.EventObjects != 18000 {
		t.Errorf("event objects = %d, want 18000", got.EventObjects)
	}
	if got.StorageObjects != 23000 {
		t.Errorf("total = %d, want 23000 — the split must not replace the total", got.StorageObjects)
	}
	// Both in the line a report leads with, because a reader given only the
	// total would divide by a number four fifths of which is Events.
	if d := got.Describe(); !strings.Contains(d, "5000 Cluster API") || !strings.Contains(d, "18000 event") {
		t.Errorf("the description does not separate them: %q", d)
	}
}

func TestTheStoredObjectSplitCountsEveryClusterApiGroup(t *testing.T) {
	// Every group a blueprint draws on stores objects, and only the core one
	// is named "cluster.x-k8s.io" exactly. A split that matched that string
	// would silently drop the control plane, bootstrap and infrastructure
	// objects — most of what a fleet creates.
	body := `go_goroutines 1
go_memstats_heap_alloc_bytes 1
apiserver_storage_objects{resource="clusters.cluster.x-k8s.io"} 1
apiserver_storage_objects{resource="kubeadmcontrolplanes.controlplane.cluster.x-k8s.io"} 2
apiserver_storage_objects{resource="kubeadmconfigs.bootstrap.cluster.x-k8s.io"} 4
apiserver_storage_objects{resource="devmachines.infrastructure.cluster.x-k8s.io"} 8
apiserver_storage_objects{resource="secrets"} 16
apiserver_storage_objects{resource="events.events.k8s.io"} 32
`
	got, err := ParseAPIServer(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.ClusterAPIObjects != 15 {
		t.Errorf("Cluster API objects = %d, want 15 (every cluster.x-k8s.io group)", got.ClusterAPIObjects)
	}
	if got.EventObjects != 32 {
		t.Errorf("event objects = %d, want 32 — the events.k8s.io group form was missed", got.EventObjects)
	}
	if got.StorageObjects != 63 {
		t.Errorf("total = %d, want 63", got.StorageObjects)
	}
}

func TestAnAPIServerBodyMissingProcessMetricsIsAnError(t *testing.T) {
	if _, err := ParseAPIServer(strings.NewReader("apiserver_current_inflight_requests 1\n")); err == nil {
		t.Error("an exposition with no process metrics parsed into a sample")
	}
}

// TestFragmentationIsTheDifferenceBetweenTwoCeilings is the reason a defrag
// belongs in this run at all.
//
// etcd's quota counts the backend file, not the live data in it. Compaction
// frees pages inside the file and returns none of them, so a fleet with heavy
// churn can hit the quota with most of the file free — etcd goes read-only and
// the run records a ceiling that is about accumulated free pages rather than
// about how much Cluster API state this store can hold. Those are different
// findings and only one of them was being looked for.
func TestFragmentationIsTheDifferenceBetweenTwoCeilings(t *testing.T) {
	got, err := ParseEtcd(strings.NewReader(etcdBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	// 3 GiB allocated, 2 GiB in use.
	if free := got.FreeBytes(); free != 1073741824 {
		t.Errorf("free = %d, want 1 GiB", free)
	}
	if share := got.Fragmentation(); share < 0.33 || share > 0.34 {
		t.Errorf("fragmentation = %v, want about 1/3", share)
	}
	if !got.Fragmented() {
		t.Error("a third of the file free was not called fragmented")
	}

	tight := got
	tight.DBInUseBytes = tight.DBTotalBytes
	if tight.Fragmented() || tight.Fragmentation() != 0 {
		t.Errorf("a fully used file was called fragmented: %+v", tight)
	}

	// The sentence a run needs when it hits the quota: whether defragmenting
	// would have bought room, or whether the store is genuinely full.
	if !strings.Contains(got.Describe(), "1.0 GiB reclaimable") {
		t.Errorf("the description does not say what a defrag would recover: %q", got.Describe())
	}

	// A store near its quota that is also badly fragmented is the case where
	// the ceiling is not the fleet's, and it has to be said outright.
	full := got
	full.DBTotalBytes = 8_000_000_000
	full.DBInUseBytes = 2_000_000_000
	if !full.NearQuota() || !full.Fragmented() {
		t.Fatalf("test setup: %+v", full)
	}
	if !strings.Contains(full.Describe(), "defragment") {
		t.Errorf("a near-quota fragmented store does not say what to do: %q", full.Describe())
	}
}

// TestTheHeapFigureSaysWhetherItIsPostCollection. Every controller's heap is
// read through pprof with gc=1, so it is the retained set. The API server's is
// read from its /metrics, and the forced collection before it is a separate,
// best-effort request — profiling can be disabled, or the authorization can
// refuse it. When that request does not land the heap figure is a point on a
// sawtooth instead, and comparing it with a controller's is comparing two
// different quantities. So the sample carries which one it is.
func TestTheHeapFigureSaysWhetherItIsPostCollection(t *testing.T) {
	got, err := ParseAPIServer(strings.NewReader(apiserverBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	collected := got
	collected.HeapCollected = true
	if strings.Contains(collected.Describe(), "not post-collection") {
		t.Errorf("a post-collection heap was caveated: %q", collected.Describe())
	}
	if d := got.Describe(); !strings.Contains(d, "not post-collection") {
		t.Errorf("a heap figure taken without a forced collection was reported as if it were "+
			"the retained set: %q", d)
	}
}
