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

	// The tail those means hide. A mean over a member that has been up for
	// days does not move for a minute of stalled writes — one read 1.6ms
	// while a manager three metres away was losing its leader lease to
	// "etcdserver: request timed out". These count the syncs that took longer
	// than slowLatency, which is the question that mean cannot answer.
	//
	// SawLatencyTail is separate because a missing bucket and a bucket
	// containing everything are the same zero. Without it, an etcd whose
	// histogram this parser did not recognise would report every single sync
	// as slow.
	WALFsyncSlow      uint64 `json:"walFsyncSlow"`
	BackendCommitSlow uint64 `json:"backendCommitSlow"`
	SawLatencyTail    bool   `json:"sawLatencyTail"`

	// Proposals are raft's unit of work, so a failed one is the direct
	// counter for the error a client sees as "etcdserver: request timed out",
	// and pending ones are the backlog that produces them.
	ProposalsFailed  uint64 `json:"proposalsFailed"`
	ProposalsPending uint64 `json:"proposalsPending"`

	// Health. A leader change under load is etcd struggling, not a topology
	// event.
	HasLeader     bool   `json:"hasLeader"`
	LeaderChanges uint64 `json:"leaderChanges"`
	SlowApplies   uint64 `json:"slowApplies"`
	SlowReads     uint64 `json:"slowReadIndexes"`
}

// slowLatency is the histogram boundary a sync is counted as slow above.
//
// It is one of etcd's own bucket edges rather than a number chosen here —
// both histograms are exponential from 1ms, so 128ms is a boundary that
// exists and needs no interpolation. It is also an order of magnitude above
// where etcd itself starts logging "slow fdatasync", which makes a sync past
// it something the store has already complained about.
const slowLatency = "0.128"

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

// FreeBytes is space inside the backend file that compaction has released and
// that only defragmentation returns.
func (e Etcd) FreeBytes() uint64 {
	if e.DBInUseBytes > e.DBTotalBytes {
		return 0
	}
	return e.DBTotalBytes - e.DBInUseBytes
}

// Fragmentation is the share of the backend file that is free.
//
// # Why a scale run cares
//
// The quota counts the file, not the live data in it. Compaction frees pages
// inside the file and returns none of them, so a fleet with heavy churn — which
// is what a converging Cluster API fleet is — can reach the quota with most of
// the file free. etcd then goes read-only, and the run records a ceiling that is
// about accumulated free pages rather than about how much state this store can
// hold. Those are different findings, and only one of them was being looked for.
func (e Etcd) Fragmentation() float64 {
	if e.DBTotalBytes == 0 {
		return 0
	}
	return float64(e.FreeBytes()) / float64(e.DBTotalBytes)
}

// Fragmented reports whether enough of the file is free to be worth reclaiming.
//
// A fifth is a judgement: below it, defragmenting buys little and costs a
// stop-the-world rewrite on the member; above it, the gap is large enough that a
// quota reached with it unreclaimed would be the wrong ceiling.
func (e Etcd) Fragmented() bool { return e.Fragmentation() > 0.20 }

// WALFsyncMeanMillis and BackendCommitMeanMillis are means rather than
// percentiles, which is what a sum and a count support. Read them with
// SlowSyncs: a mean says whether the disk is chronically slow and cannot say
// whether it stalled, and a stall is what stops a fleet.
func (e Etcd) WALFsyncMeanMillis() float64 { return mean(e.WALFsyncSum, e.WALFsyncCount) * 1000 }
func (e Etcd) BackendCommitMeanMillis() float64 {
	return mean(e.BackendCommitSum, e.BackendCommitCount) * 1000
}

// SlowSyncs is how many WAL fsyncs and backend commits took longer than
// slowLatency, and whether the reading is available at all.
func (e Etcd) SlowSyncs() (fsyncs, commits uint64, ok bool) {
	return e.WALFsyncSlow, e.BackendCommitSlow, e.SawLatencyTail
}

// Describe is what a rung carries about the store.
func (e Etcd) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s backend quota (%.0f%%), %d keys, wal fsync %.1fms, commit %.1fms",
		humanBytes(e.DBTotalBytes), humanBytes(e.QuotaBytes), 100*e.QuotaUsed(),
		e.Keys, e.WALFsyncMeanMillis(), e.BackendCommitMeanMillis())
	if e.FreeBytes() > 0 {
		fmt.Fprintf(&b, ", %s reclaimable", humanBytes(e.FreeBytes()))
	}
	if e.NearQuota() {
		b.WriteString(" — **near the quota**, at which etcd stops accepting writes")
		if e.Fragmented() {
			fmt.Fprintf(&b, ", and %.0f%% of the file is free: defragment before believing this "+
				"is the fleet's ceiling", 100*e.Fragmentation())
		}
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
	// ClusterAPIObjects is the part of it a fleet actually created: every
	// resource in a cluster.x-k8s.io group, core and providers alike.
	//
	// Separated because S3 asks what the API server costs per stored Cluster
	// API object and the total cannot answer it. In the first two runs Events
	// outnumbered Cluster API objects several to one — and Events expire on
	// their own hour-long TTL, so the total for the same fleet size moved by
	// 2x between two runs an hour apart with nothing else changed. A
	// denominator that drifts like that under a per-object figure is worse
	// than no figure, because nothing about it looks wrong.
	ClusterAPIObjects uint64 `json:"clusterApiObjects"`
	// EventObjects is the largest thing that is not the fleet, named so that a
	// reader can see how much of the total is not.
	EventObjects uint64 `json:"eventObjects"`

	// HeapCollected says whether a collection was forced before the heap
	// figure was read. Every controller's is, through pprof with gc=1, so it
	// is the retained set; the API server's needs a separate request that
	// profiling being disabled — or authorization refusing it — can lose,
	// leaving a point on a sawtooth instead. Two different quantities, so the
	// sample says which one this is rather than the report claiming both.
	//
	// Set by the sampler, not by parsing: nothing in the exposition says it.
	HeapCollected bool `json:"heapCollected"`

	// HeapSamples is how many reads the heap figure is the lowest of, when no
	// collection could be forced. See LowestHeap.
	HeapSamples int `json:"heapSamples,omitempty"`
}

// LowestHeap reduces several reads of the API server to one, keeping the
// smallest heap and everything else from the freshest read.
//
// # Why a floor rather than a collection
//
// A heap read without a forced collection is a point on a sawtooth: the first
// runs here saw the API server's heap move by 150 MiB between rungs in both
// directions while the fleet only grew. Forcing the collection needs profiling,
// and profiling cannot be turned on on this cluster — see
// hack/upstream-capi-scale/README.md for the two ClusterClass rules that make
// it impossible and the control plane that was broken finding out.
//
// The sawtooth has a floor, though, and the floor is close to the live set: the
// lowest of several reads is the best estimate available without asking the
// process for one. It is an **upper bound** — the minimum of finitely many
// samples never lands below the true post-collection figure — and it is
// described as one.
func LowestHeap(samples []APIServer) APIServer {
	if len(samples) == 0 {
		return APIServer{}
	}
	// The freshest read carries the counters, which are cumulative or current
	// rather than sawtoothed and so want the latest value, not the lowest.
	out := samples[len(samples)-1]
	out.HeapSamples = len(samples)
	for _, s := range samples {
		if s.Process.HeapAllocBytes < out.Process.HeapAllocBytes {
			out.Process.HeapAllocBytes = s.Process.HeapAllocBytes
		}
	}
	return out
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
	fmt.Fprintf(&b, "%d goroutines, %s heap, %s resident, %d objects stored "+
		"(%d Cluster API, %d event), %d requests in flight, etcd calls %.1fms",
		a.Process.Goroutines, humanBytes(a.Process.HeapAllocBytes), humanBytes(a.Process.ResidentBytes),
		a.StorageObjects, a.ClusterAPIObjects, a.EventObjects,
		a.InflightRequests, a.EtcdRequestMeanMillis())
	switch {
	case a.HeapCollected:
	case a.HeapSamples > 1:
		fmt.Fprintf(&b, " (heap is the lowest of %d reads: no collection could be forced, so this is "+
			"the sawtooth's floor and an upper bound on the retained set)", a.HeapSamples)
	default:
		b.WriteString(" (heap not post-collection: the forced collection did not land, so this is a " +
			"point on a sawtooth and not the retained set)")
	}
	if a.SheddingLoad() {
		fmt.Fprintf(&b, " — **shedding load**: %d request(s) rejected by priority and fairness",
			a.RejectedRequests)
	}
	return b.String()
}

// isClusterAPIResource matches every Cluster API group rather than the core
// one: a fleet's objects are spread over controlplane., bootstrap. and
// infrastructure. as well, and those are most of what it creates.
//
// clusterctl's own group is excluded. It writes one Provider object per
// installed provider, so a bare suffix match counts four objects on a cluster
// with no fleet at all — a constant added to the denominator of every
// per-object figure, and about a tenth of it at the small end.
func isClusterAPIResource(resource string) bool {
	if strings.HasSuffix(resource, ".clusterctl.cluster.x-k8s.io") {
		return false
	}
	return strings.HasSuffix(resource, ".cluster.x-k8s.io")
}

// isEventResource matches both names the API server stores events under.
func isEventResource(resource string) bool {
	return resource == "events" || resource == "events.events.k8s.io"
}

// ParseEtcd reads the gauges and counters that say whether the store is the
// limit.
func ParseEtcd(r io.Reader) (Etcd, error) {
	var out Etcd
	seen := false

	// The counts at or under slowLatency, kept apart from the struct so that
	// "the bucket was not there" stays distinguishable from "nothing was
	// under it". See SawLatencyTail.
	var fsyncFast, commitFast uint64
	var sawFsyncBucket, sawCommitBucket bool

	err := eachSample(r, func(name string, labels map[string]string, value float64) {
		switch name {
		case "etcd_disk_wal_fsync_duration_seconds_bucket":
			if labels["le"] == slowLatency {
				fsyncFast, sawFsyncBucket = uint64(value), true
			}
		case "etcd_disk_backend_commit_duration_seconds_bucket":
			if labels["le"] == slowLatency {
				commitFast, sawCommitBucket = uint64(value), true
			}
		case "etcd_server_proposals_failed_total":
			out.ProposalsFailed = uint64(value)
		case "etcd_server_proposals_pending":
			out.ProposalsPending = uint64(value)
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
	// Both, because a tail reported for one histogram and not the other reads
	// as a store whose commits are fine and whose syncs are not.
	if sawFsyncBucket && sawCommitBucket {
		out.SawLatencyTail = true
		out.WALFsyncSlow = countAbove(out.WALFsyncCount, fsyncFast)
		out.BackendCommitSlow = countAbove(out.BackendCommitCount, commitFast)
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
			switch resource := labels["resource"]; {
			case isClusterAPIResource(resource):
				out.ClusterAPIObjects += uint64(value)
			case isEventResource(resource):
				out.EventObjects += uint64(value)
			}
		}
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
		// The unlabelled form, which series() does not match. Its tail may
		// carry a timestamp as well — see sampleValue.
		name, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value, err := sampleValue(rest)
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

// countAbove is the tail of a histogram: everything, less the bucket at the
// boundary. Guarded because the two figures are scraped from different lines
// of one exposition and a count that arrived a bucket-write later would
// underflow a uint64 into something that reads as astronomical.
func countAbove(total, atOrUnder uint64) uint64 {
	if atOrUnder >= total {
		return 0
	}
	return total - atOrUnder
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
