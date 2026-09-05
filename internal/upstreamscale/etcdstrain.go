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
	"fmt"
	"sort"
	"strings"
)

// EtcdSince says what the store did while the run was climbing, against the
// baseline taken before any fleet existed.
//
// # The failure this is the answer to
//
// A stock climb held 500 clusters and then a manager died:
//
//	leaderelection.go:454 "Failed to update lease optimistically, falling back
//	to slow path" err="etcdserver: request timed out"
//	leaderelection.go:304 "Failed to renew lease" err="context deadline exceeded"
//	main.go:415 "Problem running manager" err="leader election lost"
//
// A leader lease is the smallest write a cluster makes. An etcd that cannot
// commit one is not a manager that needs a longer renew deadline, and every
// figure the run reported up to that point was taken while the store was in
// that state. The report already sampled every one of these counters, member
// by member, and nothing read them back on a failure — so the answer was in
// the report and the run said "restarted (Error)".
//
// # Counters rather than thresholds
//
// Every signal here is a difference against the baseline, and none of them
// carries a limit this code invents. A leader change and a slow apply are
// etcd's own judgements, already made, and counting them needs no opinion
// about how slow is too slow. The latencies are printed beside their baseline
// for the same reason: a backend commit that went from 4ms to 400ms says what
// it needs to say without being called bad.
//
// The counters are monotonic within one member's process life. A member that
// restarted resets them, which shows up as a negative difference — reported as
// the restart it is rather than clamped to zero, because a store that lost a
// member is the loudest thing on this list.
func EtcdSince(before, now map[string]Etcd) string {
	names := make([]string, 0, len(now))
	for name := range now {
		names = append(names, name)
	}
	sort.Strings(names)

	var strained []string
	for _, name := range names {
		was, had := before[name]
		is := now[name]

		var notes []string
		switch {
		case had && is.LeaderChanges < was.LeaderChanges,
			had && is.SlowApplies < was.SlowApplies:
			notes = append(notes, "its counters went backwards, so this member restarted "+
				"during the run and the store lost it for as long as that took")
		case had && is.LeaderChanges > was.LeaderChanges:
			notes = append(notes, fmt.Sprintf("%d leader change(s)",
				is.LeaderChanges-was.LeaderChanges))
		}
		if had && is.SlowApplies > was.SlowApplies {
			notes = append(notes, fmt.Sprintf("%d slow applies", is.SlowApplies-was.SlowApplies))
		}
		if !is.HasLeader {
			notes = append(notes, "it has no leader right now")
		}
		if had {
			if during, ok := sinceMeanMillis(is.BackendCommitSum, was.BackendCommitSum,
				is.BackendCommitCount, was.BackendCommitCount); ok && during > was.BackendCommitMeanMillis() {
				notes = append(notes, fmt.Sprintf("backend commit averaged %.0fms during the run "+
					"against %.0fms before it", during, was.BackendCommitMeanMillis()))
			}
			if during, ok := sinceMeanMillis(is.WALFsyncSum, was.WALFsyncSum,
				is.WALFsyncCount, was.WALFsyncCount); ok && during > was.WALFsyncMeanMillis() {
				notes = append(notes, fmt.Sprintf("WAL fsync averaged %.0fms against %.0fms",
					during, was.WALFsyncMeanMillis()))
			}
		}
		if is.NearQuota() {
			notes = append(notes, fmt.Sprintf("its backend is %.0f%% of the quota, and a store that "+
				"reaches it goes read-only for the whole cluster", is.QuotaUsed()*100))
		}
		if len(notes) > 0 {
			strained = append(strained, name+": "+strings.Join(notes, ", "))
		}
	}

	if len(strained) == 0 {
		return ""
	}
	return "etcd, since the run began — " + strings.Join(strained, "; ")
}

// sinceMeanMillis is the mean over the run rather than over the member's life.
//
// Both figures are cumulative, so a lifetime mean taken at the top of a climb
// is diluted by every fast commit made before the fleet existed — on a member
// that has been up for days, a period of 400ms commits barely moves it. The
// difference of the sums over the difference of the counts is the mean of the
// interval, which is what the run is asking about.
//
// Not ok when nothing was committed in the interval, or when the counters went
// backwards because the member restarted — a rate cannot be recovered from
// either, and the restart is reported on its own.
func sinceMeanMillis(nowSum, wasSum float64, nowCount, wasCount uint64) (float64, bool) {
	if nowCount <= wasCount || nowSum < wasSum {
		return 0, false
	}
	return (nowSum - wasSum) / float64(nowCount-wasCount) * 1000, true
}
