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

func TestAnAPIServerBodyMissingProcessMetricsIsAnError(t *testing.T) {
	if _, err := ParseAPIServer(strings.NewReader("apiserver_current_inflight_requests 1\n")); err == nil {
		t.Error("an exposition with no process metrics parsed into a sample")
	}
}
