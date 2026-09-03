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
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// # Why the control plane is sampled at all, and first
//
// The kcp runs found the store, not the controllers, was what ran out: at 200
// clusters of fifty nodes the shard was OOM killed against 4 GiB while the four
// managers sat at a fifth of their own limits. There is no reason to expect a
// kube-apiserver in front of etcd to behave differently, and the controllers
// here are the tunable half — concurrency, QPS, burst and resources are all
// flags.
//
// So a rung that fails needs the control plane's numbers beside it, or the
// finding is "Cluster API stopped at N" when the truth is "this etcd stopped at
// N". Those are different sentences and only one of them is useful.

// nearQuota is the share of etcd's backend quota above which the store is
// reported as approaching its ceiling.
//
// Not 100%: etcd stops accepting writes at the quota and the cluster is over
// by then. 85% is far enough ahead to be a warning and close enough to be true.
const nearQuota = 0.85

// Etcd is the store, measured against its own limits.
type Etcd struct {
	// DBTotalBytes is the backend file, including space freed by compaction
	// but not returned until defragmentation. This is what the quota counts.
	DBTotalBytes uint64 `json:"dbTotalBytes"`
	// DBInUseBytes is the part still holding live data. The gap between the
	// two is what a defragmentation would recover.
	DBInUseBytes uint64 `json:"dbInUseBytes"`
	// QuotaBytes is the ceiling. A size without it is not a finding.
	QuotaBytes uint64 `json:"quotaBytes"`
	// Keys is every key, revisions included — what grows under a burst of
	// status updates rather than under a bigger fleet.
	Keys uint64 `json:"keys"`

	// The two latencies that say the disk is the limit rather than the fleet.
	WALFsyncSum        float64 `json:"walFsyncSeconds"`
	WALFsyncCount      uint64  `json:"walFsyncCount"`
	BackendCommitSum   float64 `json:"backendCommitSeconds"`
	BackendCommitCount uint64  `json:"backendCommitCount"`

	// Health. A leader change under load is etcd struggling, not a topology
	// event.
	HasLeader     bool   `json:"hasLeader"`
	LeaderChanges uint64 `json:"leaderChanges"`
	SlowApplies   uint64 `json:"slowApplies"`
	SlowReads     uint64 `json:"slowReadIndexes"`
}

// QuotaUsed is the share of the backend quota the database occupies.
func (e Etcd) QuotaUsed() float64 {
	if e.QuotaBytes == 0 {
		return 0
	}
	return float64(e.DBTotalBytes) / float64(e.QuotaBytes)
}

// NearQuota reports whether the store is approaching the ceiling at which etcd
// stops accepting writes.
func (e Etcd) NearQuota() bool { return e.QuotaUsed() > nearQuota }

// WALFsyncMeanMillis and BackendCommitMeanMillis are means rather than
// percentiles: a histogram's buckets are not in this parser, and what matters
// here is a figure that moves by an order of magnitude between rungs.
func (e Etcd) WALFsyncMeanMillis() float64 { return mean(e.WALFsyncSum, e.WALFsyncCount) * 1000 }
func (e Etcd) BackendCommitMeanMillis() float64 {
	return mean(e.BackendCommitSum, e.BackendCommitCount) * 1000
}

// Describe is what a rung carries about the store.
func (e Etcd) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s backend quota (%.0f%%), %d keys, wal fsync %.1fms, commit %.1fms",
		humanBytes(e.DBTotalBytes), humanBytes(e.QuotaBytes), 100*e.QuotaUsed(),
		e.Keys, e.WALFsyncMeanMillis(), e.BackendCommitMeanMillis())
	if e.NearQuota() {
		b.WriteString(" — **near the quota**, at which etcd stops accepting writes")
	}
	if !e.HasLeader {
		b.WriteString(" — **no leader**")
	}
	if e.LeaderChanges > 0 {
		fmt.Fprintf(&b, ", %d leader change(s)", e.LeaderChanges)
	}
	return b.String()
}

// APIServer is what the API server costs and whether it is coping.
type APIServer struct {
	// Process is the same three quantities every other component reports, so
	// the control plane sits in the same table as the controllers.
	Process deployedscale.ProcessSample `json:"process"`

	// InflightRequests is both kinds summed: split by kind they are two halves
	// of one pressure.
	InflightRequests uint64 `json:"inflightRequests"`
	// RejectedRequests is priority and fairness shedding load. Any at all is
	// worth a reader's attention.
	RejectedRequests uint64 `json:"rejectedRequests"`

	// EtcdRequest* is the API server's own view of how slow the store is
	// being, which is the clearest single number for "the store is the limit".
	EtcdRequestSum   float64 `json:"etcdRequestSeconds"`
	EtcdRequestCount uint64  `json:"etcdRequestCount"`

	// StorageObjects is everything the store holds, summed over resources.
	StorageObjects uint64 `json:"storageObjects"`
}

// SheddingLoad reports whether the API server has rejected any request. A rung
// that failed while this is true failed for a reason no controller's numbers
// would show.
func (a APIServer) SheddingLoad() bool { return a.RejectedRequests > 0 }

// EtcdRequestMeanMillis is how long the API server's calls into the store take.
func (a APIServer) EtcdRequestMeanMillis() float64 {
	return mean(a.EtcdRequestSum, a.EtcdRequestCount) * 1000
}

// Describe is what a rung carries about the API server.
func (a APIServer) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d goroutines, %s heap, %s resident, %d objects stored, %d requests in flight, "+
		"etcd calls %.1fms",
		a.Process.Goroutines, humanBytes(a.Process.HeapAllocBytes), humanBytes(a.Process.ResidentBytes),
		a.StorageObjects, a.InflightRequests, a.EtcdRequestMeanMillis())
	if a.SheddingLoad() {
		fmt.Fprintf(&b, " — **shedding load**: %d request(s) rejected by priority and fairness",
			a.RejectedRequests)
	}
	return b.String()
}

// ParseEtcd reads the gauges and counters that say whether the store is the
// limit.
func ParseEtcd(r io.Reader) (Etcd, error) {
	var out Etcd
	seen := false

	err := eachSample(r, func(name string, _ map[string]string, value float64) {
		switch name {
		case "etcd_mvcc_db_total_size_in_bytes":
			out.DBTotalBytes, seen = uint64(value), true
		case "etcd_mvcc_db_total_size_in_use_in_bytes":
			out.DBInUseBytes = uint64(value)
		case "etcd_server_quota_backend_bytes":
			out.QuotaBytes, seen = uint64(value), true
		case "etcd_debugging_mvcc_keys_total":
			out.Keys = uint64(value)
		case "etcd_disk_wal_fsync_duration_seconds_sum":
			out.WALFsyncSum = value
		case "etcd_disk_wal_fsync_duration_seconds_count":
			out.WALFsyncCount = uint64(value)
		case "etcd_disk_backend_commit_duration_seconds_sum":
			out.BackendCommitSum = value
		case "etcd_disk_backend_commit_duration_seconds_count":
			out.BackendCommitCount = uint64(value)
		case "etcd_server_has_leader":
			out.HasLeader = value > 0
		case "etcd_server_leader_changes_seen_total":
			out.LeaderChanges = uint64(value)
		case "etcd_server_slow_apply_total":
			out.SlowApplies = uint64(value)
		case "etcd_server_slow_read_indexes_total":
			out.SlowReads = uint64(value)
		}
	})
	if err != nil {
		return Etcd{}, err
	}
	if !seen {
		return Etcd{}, errors.New("no etcd gauges in this exposition: etcd serves its metrics on " +
			"--listen-metrics-urls, which kubeadm points at 127.0.0.1 by default — the ClusterClass " +
			"patch opens it so the run can be measured")
	}
	return out, nil
}

// ParseAPIServer reads the API server's cost and its two pressure signals.
func ParseAPIServer(r io.Reader) (APIServer, error) {
	var out APIServer
	var sawGoroutines, sawHeap bool

	err := eachSample(r, func(name string, labels map[string]string, value float64) {
		switch name {
		case "go_goroutines":
			out.Process.Goroutines, sawGoroutines = int(value), true
		case "go_memstats_heap_alloc_bytes":
			out.Process.HeapAllocBytes, sawHeap = uint64(value), true
		case "go_memstats_sys_bytes":
			out.Process.HeapSysBytes = uint64(value)
		case "process_resident_memory_bytes":
			out.Process.ResidentBytes = uint64(value)
		case "process_cpu_seconds_total":
			out.Process.CPUSeconds = value
		case "apiserver_current_inflight_requests":
			out.InflightRequests += uint64(value)
		case "apiserver_flowcontrol_rejected_requests_total":
			out.RejectedRequests += uint64(value)
		case "etcd_request_duration_seconds_sum":
			out.EtcdRequestSum += value
		case "etcd_request_duration_seconds_count":
			out.EtcdRequestCount += uint64(value)
		case "apiserver_storage_objects":
			out.StorageObjects += uint64(value)
		}
		_ = labels
	})
	if err != nil {
		return APIServer{}, err
	}
	if !sawGoroutines || !sawHeap {
		return APIServer{}, errors.New("no process metrics in this exposition: the API server serves " +
			"them on its own /metrics, so a body without them is not the API server's")
	}
	return out, nil
}

// eachSample walks a Prometheus text exposition, calling f for every sample.
func eachSample(r io.Reader, f func(name string, labels map[string]string, value float64)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if name, labels, value, ok := series(line); ok {
			f(name, labels, value)
			continue
		}
		// The unlabelled form, which series() does not match.
		name, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			continue
		}
		f(name, nil, value)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading the exposition: %w", err)
	}
	return nil
}

func mean(sum float64, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// humanBytes mirrors the report's own formatting so a control plane figure
// reads the same as a controller's.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
