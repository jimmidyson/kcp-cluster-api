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

// TestAStoreThatStalledIsNamedAsOne.
//
// The run this is from: a manager lost its leader lease to "etcdserver:
// request timed out" and exited, and the run reported a restart. A leader
// lease is the smallest write a cluster makes, so an etcd that cannot commit
// one is the answer — and every counter needed to say so was already in the
// report, unread.
func TestAStoreThatStalledIsNamedAsOne(t *testing.T) {
	before := map[string]Etcd{"etcd-cp-0": {
		HasLeader: true, LeaderChanges: 2, SlowApplies: 5,
		BackendCommitSum: 4, BackendCommitCount: 1000, // 4ms
		WALFsyncSum: 2, WALFsyncCount: 1000, // 2ms
	}}
	now := map[string]Etcd{"etcd-cp-0": {
		HasLeader: true, LeaderChanges: 5, SlowApplies: 900,
		BackendCommitSum: 44, BackendCommitCount: 1100, // 400ms over the run
		WALFsyncSum: 22, WALFsyncCount: 1100, // 200ms over the run
	}}

	got := EtcdSince(before, now)
	if !strings.Contains(got, "3 leader change(s)") {
		t.Errorf("leader changes during the run are missing: %q", got)
	}
	if !strings.Contains(got, "895 slow applies") {
		t.Errorf("slow applies during the run are missing: %q", got)
	}
	// The mean over the interval, not over the member's life: a lifetime mean
	// would read 40ms here and look survivable.
	if !strings.Contains(got, "400ms") {
		t.Errorf("the commit latency is diluted by the baseline: %q", got)
	}
	if !strings.Contains(got, "4ms before it") {
		t.Errorf("there is nothing to compare the latency against: %q", got)
	}
}

// TestAQuietStoreSaysNothing, so a rung that failed for a reason other than
// the store is not given a paragraph about the store.
func TestAQuietStoreSaysNothing(t *testing.T) {
	same := Etcd{
		HasLeader: true, LeaderChanges: 2, SlowApplies: 5,
		BackendCommitSum: 4, BackendCommitCount: 1000,
		WALFsyncSum: 2, WALFsyncCount: 1000, QuotaBytes: 8 << 30, DBTotalBytes: 1 << 30,
	}
	steady := same
	steady.BackendCommitCount, steady.BackendCommitSum = 2000, 8 // still 4ms
	steady.WALFsyncCount, steady.WALFsyncSum = 2000, 4

	if got := EtcdSince(map[string]Etcd{"etcd-cp-0": same},
		map[string]Etcd{"etcd-cp-0": steady}); got != "" {
		t.Errorf("a store that did nothing was reported as strained: %q", got)
	}
}

// TestAMemberThatRestartedIsNotReadAsAFastOne. Every counter here is
// cumulative over one process's life, so a member that restarted has counters
// lower than its own baseline — subtracting would underflow a uint64 into
// hundreds of quintillions, and clamping to zero would report the loudest
// event on the list as nothing at all.
func TestAMemberThatRestartedIsNotReadAsAFastOne(t *testing.T) {
	before := map[string]Etcd{"etcd-cp-0": {HasLeader: true, LeaderChanges: 9, SlowApplies: 400}}
	now := map[string]Etcd{"etcd-cp-0": {HasLeader: true, LeaderChanges: 0, SlowApplies: 0}}

	got := EtcdSince(before, now)
	if !strings.Contains(got, "restarted") {
		t.Errorf("a member that restarted mid-run was not reported: %q", got)
	}
	if strings.Contains(got, "18446744073709") {
		t.Errorf("the counter difference underflowed: %q", got)
	}
}

// TestAStoreWithNoLeaderIsSaidToHaveNone, which is the state in which nothing
// in the cluster can write at all.
func TestAStoreWithNoLeaderIsSaidToHaveNone(t *testing.T) {
	got := EtcdSince(
		map[string]Etcd{"etcd-cp-0": {HasLeader: true}},
		map[string]Etcd{"etcd-cp-0": {HasLeader: false}},
	)
	if !strings.Contains(got, "no leader") {
		t.Errorf("a store with no leader reads as healthy: %q", got)
	}
}

// TestApproachingTheQuotaIsSaidPlainly. Reaching it turns the whole cluster
// read-only, which presents as every controller stopping at once and is not a
// capacity finding about Cluster API.
func TestApproachingTheQuotaIsSaidPlainly(t *testing.T) {
	before := map[string]Etcd{"etcd-cp-0": {HasLeader: true, QuotaBytes: 2 << 30, DBTotalBytes: 1 << 20}}
	nearlyFull := uint64(2<<30) / 100 * 95
	now := map[string]Etcd{"etcd-cp-0": {HasLeader: true, QuotaBytes: 2 << 30,
		DBTotalBytes: nearlyFull}}

	got := EtcdSince(before, now)
	if !strings.Contains(got, "quota") || !strings.Contains(got, "read-only") {
		t.Errorf("a store about to go read-only was not called out: %q", got)
	}
}

// TestAMemberWithNoBaselineIsNotInvented. A member that joined mid-run, or one
// the baseline read failed for, has nothing to be compared against — reporting
// its lifetime counters as this run's would attribute the cluster's whole
// history to the climb.
func TestAMemberWithNoBaselineIsNotInvented(t *testing.T) {
	now := map[string]Etcd{"etcd-cp-2": {HasLeader: true, LeaderChanges: 40, SlowApplies: 9000}}
	if got := EtcdSince(map[string]Etcd{}, now); strings.Contains(got, "9000") {
		t.Errorf("a member's whole history was reported as this run's: %q", got)
	}
}
