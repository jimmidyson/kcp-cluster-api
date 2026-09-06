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
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Runner climbs a ladder against one Target and writes one report.
//
// It is the whole measurement: preflight, settle, a defragmented baseline, a
// doubling ladder with a defragmentation between rungs and never inside one,
// a failure classified rather than announced, and a soak of the largest fleet
// that converged. Both sides of the comparison run this same code, which is
// the point — see Target.
type Runner struct {
	// Target is the side under test.
	Target Target
	// Host is the cluster the processes run on, which is the same cluster on
	// both sides and is not where the fleet lives. See Target.
	Host client.Client
	// Sampler and Defragmenter read and maintain whatever Target.Store names.
	Sampler      *Sampler
	Defragmenter *Defragmenter

	Options RunOptions
	// Logf is where progress goes. Nil is silent.
	Logf func(string, ...any)

	// Created is every tenant the run made, in creation order, for a caller
	// that wants to tear down after a failure as well as after a success.
	Created []string

	// controlPlaneAtStart is what the control plane's pods had already been
	// through before the climb began, so that the health check reports what
	// this run did rather than what the cluster remembers.
	controlPlaneAtStart map[string]deployedscale.PodFacts

	// etcdAtStart is the same for the store, and for the same reason: every
	// counter here is cumulative over a member's process life, so on a
	// long-lived cluster the raw numbers are mostly other runs.
	etcdAtStart map[string]Etcd
	// store is where that etcd is, kept so a failure can be diagnosed from
	// wherever it is noticed rather than only where the ladder can see it.
	store StoreLocation
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Teardown removes what the run created. Safe to call when nothing was.
func (r *Runner) Teardown(ctx context.Context) error {
	return r.Target.Teardown(ctx, r.Created, r.Options.TeardownTimeout, r.Options.PollInterval, r.Logf)
}

// Run is the measurement. It returns the report even when the climb stopped
// early, because a climb that found a ceiling is a result whichever rung it
// stopped at — and an error only when it measured nothing at all.
func (r *Runner) Run(ctx context.Context) (*deployedscale.Report, Ceiling, error) {
	opts := r.Options

	if err := r.Target.Prepare(ctx); err != nil {
		return nil, Ceiling{}, fmt.Errorf("this cluster cannot serve what the run creates:\n%w", err)
	}

	report := &deployedscale.Report{Title: r.Target.Title(opts.StartClusters, opts.NodesPerCluster)}
	for k, v := range r.Target.Facts() {
		report.AddFact(k, v)
	}
	report.AddFact("endState", "every control plane ready and every Machine Ready")
	report.AddFact("nodesPerCluster", fmt.Sprint(opts.NodesPerCluster))
	report.AddFact("heapSample", "every controller's is read through pprof with gc=1, so live heap is "+
		"the retained set; the control plane's line says for itself, since the collection it needs is a "+
		"separate best-effort request")
	if opts.DriverFact != "" {
		report.AddFact("driver", opts.DriverFact)
	}

	controllers := r.Target.Controllers()
	store := r.Target.Store()

	sample := func(label string, clusters, machines int) {
		components, throttling, err := r.Sampler.Sample(ctx, r.Host, controllers)
		if err != nil {
			r.logf("NOTE: could not sample the controllers at %s: %v", label, err)
			return
		}
		if cp, described, err := r.Target.ControlPlane(ctx, r.Host, opts.APIHeapSamples, opts.APIHeapGap); err == nil {
			components = append(components, cp...)
			report.AddFact("controlPlane@"+label, described)
		} else {
			r.logf("NOTE: could not sample the control plane at %s: %v", label, err)
		}
		// Every member, not the first: the backend size is shared but the disk
		// latencies, the leader changes and what a defragmentation would
		// reclaim are each one machine's.
		members, err := r.Sampler.EveryEtcdMember(ctx, r.Host, store)
		if len(members) > 0 {
			report.AddFact("etcd@"+label, DescribeEtcdMembers(members))
			for name, member := range members {
				if member.NearQuota() {
					r.logf("WARNING at %s: %s %s", label, name, member.Describe())
				}
			}
		}
		if err != nil {
			r.logf("NOTE: reading etcd at %s: %v", label, err)
		}
		for name, th := range throttling {
			if th.Significant() {
				report.AddFact("throttling@"+label+"/"+name, th.Describe())
			}
		}
		report.Add(deployedscale.Sample{
			Label: label, Workspaces: clusters, Clusters: clusters, Nodes: machines,
			Components: components,
		})
	}

	// The controllers have to have finished starting, or the baseline is of a
	// manager still opening its caches and every slope measured from it is
	// inflated. Reported either way rather than fatal: a moving baseline is a
	// caveat on the numbers and is worth more than no run.
	if settle, err := WaitForSettled(ctx, r.Sampler, r.Host, controllers,
		opts.SettleTolerance, opts.SettleTimeout, opts.PollInterval); err != nil {
		r.logf("NOTE: could not wait for the controllers to settle: %v", err)
	} else {
		report.AddFact("baseline", settle.Describe())
		r.logf("%s", settle.Describe())
	}

	// What the control plane has already been through, before anything is
	// created. Every restart the health check reports from here is one this run
	// caused; without it, a kubeadm control plane pod restarted at any point in
	// the node's life would fail the first rung.
	if facts, err := r.Sampler.ControlPlaneFacts(ctx, r.Host); err == nil {
		r.controlPlaneAtStart = facts
		if restarted := Classify(HealthOf(facts), false); restarted != "" {
			report.AddFact("controlPlaneHistory", restarted)
			r.logf("NOTE: the control plane carries history from before this run: %s", restarted)
		}
	} else {
		r.logf("NOTE: could not read the control plane's pods (%v), so a process dying during the "+
			"run will not be noticed as one", err)
	}

	r.defragment(ctx, report, store, "baseline")

	// After the defragmentation, so that the rewrite it just did is not
	// counted as strain the climb caused.
	r.store = store
	if members, err := r.Sampler.EveryEtcdMember(ctx, r.Host, store); err == nil {
		r.etcdAtStart = members
	} else {
		r.logf("NOTE: could not read etcd's counters (%v), so a store that stalls under the "+
			"fleet will not be reported as the reason", err)
	}

	// The baseline, before any fleet exists. Every slope this run reports is a
	// difference between two large numbers, and without this the smaller of
	// them is still a fleet.
	sample("baseline (no clusters)", 0, 0)

	var rungs []RungResult
	held := 0
	for i, clusters := range Ladder(opts.StartClusters, opts.MaxClusters, opts.RungStep) {
		fleet, err := r.Target.Plan(clusters)
		if err != nil {
			return report, Summarise(rungs), fmt.Errorf("rung of %d clusters: %w", clusters, err)
		}
		machines := fleet.Machines()

		// Between rungs, never inside one: a defragmentation is a
		// stop-the-world rewrite on the member it runs against.
		//
		// It is also the reason the creates that follow are retried through
		// transient rejections. A member that has just been rewritten can drop
		// its watches, and a manager whose informers are re-listing refuses
		// admission until they have synced — which arrives as a rejection of
		// the first Cluster of the next rung and looks exactly like a ceiling.
		// See Transient.
		if i > 0 {
			r.defragment(ctx, report, store, fmt.Sprint(clusters))
		}

		r.logf("=== rung: %d clusters, %d Machines", clusters, machines)

		// Creation is timed apart from convergence, because the driver
		// applying a rung's objects is itself work and a total that cannot be
		// split is not a measurement of the system under test.
		startedCreate := time.Now()
		madeTenants, err := r.Target.Create(ctx, fleet, opts.CreateConcurrency)
		r.Created = append(r.Created, madeTenants...)
		if err != nil {
			// With whatever the cluster was doing at the time. A creation
			// that is refused because a manager is restarting and a creation
			// that is refused because the cluster is full read the same in
			// an API error, and only one of them is a ceiling.
			failure := "the fleet could not be created: " + err.Error()
			if why := r.died(ctx, controllers); why != "" {
				failure += " — and " + why
			}
			if strain := r.strain(ctx); strain != "" {
				failure += " — " + strain
			}
			rungs = append(rungs, RungResult{
				Clusters: clusters, Machines: machines, Added: clusters - held,
				CreatedIn: time.Since(startedCreate),
				Failure:   failure,
			})
			break
		}
		createdIn := time.Since(startedCreate)
		r.logf("    created in %s", createdIn.Round(time.Second))

		startedWait := time.Now()
		converged, why := r.wait(ctx, controllers, clusters, machines)
		rung := RungResult{
			Clusters: clusters, Machines: machines, Added: clusters - held,
			Converged: converged,
			CreatedIn: createdIn, WaitedFor: time.Since(startedWait),
		}
		held = clusters

		label := fmt.Sprintf("%d clusters", clusters)
		if !converged {
			rung.Failure = why
			label += " (did not converge)"
		}
		sample(label, clusters, machines)
		report.AddFact(fmt.Sprintf("rung@%d", clusters), rung.Timing())
		r.logf("    %s", rung.Timing())
		rungs = append(rungs, rung)
		if !converged {
			break
		}
	}

	ceiling := Summarise(rungs)
	report.AddFact("ceiling", ceiling.Describe())
	r.logf("%s", ceiling.Describe())

	// Reaching a fleet and holding it are different questions.
	if ceiling.LastGood != nil && opts.Soak > 0 {
		r.soak(ctx, report, sample, ceiling)
	}

	if ceiling.LastGood == nil {
		return report, ceiling, fmt.Errorf("measured nothing: %s", ceiling.Describe())
	}
	return report, ceiling, nil
}

// defragment runs one round against the target's store and records what it
// reclaimed, either way: a store that would not defragment is the one whose
// file reaches the quota first, which is worth knowing and is not a reason to
// abandon a climb.
func (r *Runner) defragment(ctx context.Context, report *deployedscale.Report, store StoreLocation, at string) {
	results, err := r.Defragmenter.AllAt(ctx, r.Host, r.Sampler, store)
	if err != nil {
		r.logf("NOTE: could not defragment before %s: %v", at, err)
		return
	}
	report.AddFact("defrag@"+at, DescribeDefrag(results))
	r.logf("%s", DescribeDefrag(results))
}

// died reports the first component that has stopped since this run began, or
// "" when everything is still up.
//
// The managers and the control plane both, because a run aimed at a ceiling has
// to be able to say the API server was OOM killed rather than that
// reconciliation stopped keeping up. Cheap by construction: the control-plane
// half reads pod status and no metrics.
//
// The control plane is judged against the baseline rather than against zero.
// Its pods live as long as the node, so their restart counts carry every
// earlier run's history, and checking the raw number would fail the first rung
// on any cluster that has been pushed before. See HealthSince.
//
// Errors are swallowed. This is a diagnosis attached to a failure that has
// already happened, and a run that turned "could not read the pods" into a
// second failure would bury the first.
func (r *Runner) died(ctx context.Context, controllers []Controller) string {
	if components, throttling, err := r.Sampler.Sample(ctx, r.Host, controllers); err == nil {
		if why := Classify(components, false); why != "" {
			// With the kernel's own accounting for the component that died.
			// A manager killed while starved of quota and one killed with CPU
			// to spare are different findings, and the sample already carries
			// the difference — died() used to discard it, so establishing
			// which was a scrape by hand after the run was over.
			if th, ok := throttling[Culprit(components)]; ok && th.Periods > 0 {
				why += " — " + th.Describe()
				if !th.Significant() {
					why += ", so it was not short of CPU"
				}
			}
			return why
		}
	}
	if facts, err := r.Sampler.ControlPlaneFacts(ctx, r.Host); err == nil {
		if why := Classify(HealthSince(r.controlPlaneAtStart, facts), false); why != "" {
			return why
		}
	}
	return ""
}

// strain reports what the store did while the run was climbing, or "" when it
// did nothing worth saying.
//
// Attached to a failure rather than checked as one. A leader change is not by
// itself a reason to stop a climb — etcd elects a new leader and carries on —
// but a rung that failed while the store was electing leaders and stalling
// commits has its answer there, and the alternative is what happened the first
// time: the counters were in the report and the run said "restarted (Error)".
//
// Errors are swallowed for the same reason they are in died: this is a
// diagnosis attached to a failure that has already happened.
func (r *Runner) strain(ctx context.Context) string {
	if r.etcdAtStart == nil {
		return ""
	}
	members, err := r.Sampler.EveryEtcdMember(ctx, r.Host, r.store)
	if err != nil {
		return ""
	}
	return EtcdSince(r.etcdAtStart, members)
}

// wait polls until the rung reaches the end state, or a component dies, or
// time runs out — and says which.
func (r *Runner) wait(ctx context.Context, controllers []Controller, clusters, machines int) (bool, string) {
	deadline := time.Now().Add(r.Options.StepTimeout)
	var last Convergence
	var steady Steadiness

	for {
		var err error
		last, err = r.Target.Converged(ctx, clusters, machines)
		if err != nil {
			return false, "counting the fleet: " + err.Error()
		}
		if last.Done {
			return true, ""
		}
		steady.Observe(last)

		// A component that died is why the fleet has not arrived, rather than
		// a second thing that went wrong. Checked every poll so that a kill is
		// reported when it happens rather than after the step timeout.
		if why := r.died(ctx, controllers); why != "" {
			return false, why
		}

		if time.Now().After(deadline) {
			// A fleet that never arrived and a fleet that arrived and would
			// not hold still are different findings, and the last poll's
			// count cannot tell them apart. See Steadiness.
			why := fmt.Sprintf("%s (%s)", timedOutBecause(steady), last.Describe())
			if flapped := steady.Describe(); flapped != "" {
				why += " — " + flapped
			}
			if strain := r.strain(ctx); strain != "" {
				why += " — " + strain
			}
			return false, why
		}
		r.logf("    %s", last.Describe())
		select {
		case <-ctx.Done():
			return false, "interrupted: " + last.Describe()
		case <-time.After(r.Options.PollInterval):
		}
	}
}

// soak holds the largest fleet that converged, sampling throughout.
func (r *Runner) soak(ctx context.Context, report *deployedscale.Report,
	sample func(string, int, int), ceiling Ceiling,
) {
	opts := r.Options
	r.logf("=== soak: holding %d clusters for %s", ceiling.LastGood.Clusters, opts.Soak)
	before := len(report.Samples)
	deadline := time.Now().Add(opts.Soak)
	for n := 0; ; n++ {
		sample(fmt.Sprintf("soak %s", time.Duration(n)*opts.SoakInterval),
			ceiling.LastGood.Clusters, ceiling.LastGood.Machines)
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			r.logf("NOTE: the soak was interrupted")
			return
		case <-time.After(opts.SoakInterval):
		}
	}

	// Ready at the end, which no process metric shows.
	ready := 0
	if final, err := r.Target.Converged(ctx, ceiling.LastGood.Clusters, ceiling.LastGood.Machines); err == nil {
		ready = final.ControlPlanesReady
	}
	drift := Drift(report.Samples[before:], ceiling.LastGood.Clusters, ready)
	report.AddFact("soak", drift.Describe())
	r.logf("%s", drift.Describe())
}
