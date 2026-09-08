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
	// besideAtStart is the same for what runs beside the control plane rather
	// than being it — a cloud controller manager, a CNI, a metrics agent. Kept
	// apart because a death there is worth reporting and is not this run's
	// ceiling. See IsControlPlaneComponent.
	besideAtStart map[string]deployedscale.PodFacts
	// managersAtStart is the same for the managers. They were left out when
	// the control plane got one, and a manager that lost its leader election
	// in one run then failed every rung of every run after it. See
	// ManagersSince.
	managersAtStart map[string]int32
	// cpRestartsAtStart is the same again for everything on the control
	// plane's nodes, keyed by pod rather than by component: the readout covers
	// every process up there, including the ones that merely run beside the
	// control plane and so have no entry in controlPlaneAtStart. Taken from
	// the first readout of the run rather than beforehand, because that
	// readout is a scrape of three nodes and one is enough. See Restarted.
	cpRestartsAtStart map[string]int32

	// etcdBaseline is the same for the store, and for the same reason: every
	// counter here is cumulative over a member's process life, so on a
	// long-lived cluster the raw numbers are mostly other runs. Retaken after
	// every defragmentation rather than once, because a defragmentation is
	// strain of its own and a rung should not carry the one before it. See
	// EtcdSince.
	etcdBaseline map[string]Etcd
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
	report.AddFact("endState", "every control plane Available with every replica ready, and every "+
		"Machine Ready")
	report.AddFact("nodesPerCluster", fmt.Sprint(opts.NodesPerCluster))
	report.AddFact("heapSample", "every controller's is read through pprof with gc=1, so live heap is "+
		"the retained set; the control plane's line says for itself, since the collection it needs is a "+
		"separate best-effort request")
	if opts.DriverFact != "" {
		report.AddFact("driver", opts.DriverFact)
	}

	controllers := r.Target.Controllers()
	store := r.Target.Store()
	started := time.Now()

	sample := func(label string, clusters, machines int) {
		components, throttling, err := r.Sampler.Sample(ctx, r.Host, controllers)
		if err != nil {
			r.logf("NOTE: could not sample the controllers at %s: %v", label, err)
			return
		}
		// Against the baseline, exactly as the health check is: a pod's own
		// count is its whole life, and a kubeadm static pod's life is the
		// node's. Rebased here rather than in the readout so that the samples
		// the report keeps carry this run's restarts too — the report's own
		// "a container restarted during this run" banner reads them.
		components = RestartsSince(r.managersAtStart, components)
		// Which of them shares a node with the control plane, said at every
		// sample rather than once, because a rescheduled manager can land
		// there at any rung. See OnControlPlane.
		if nodes, err := ControlPlaneNodes(ctx, r.Host); err == nil {
			if warning := OnControlPlane(components, nodes); warning != "" {
				report.AddFact("managersOnControlPlane@"+label, warning)
				r.logf("WARNING at %s: %s", label, warning)
			}
		}
		if cp, described, err := r.Target.ControlPlane(ctx, r.Host, opts.APIHeapSamples, opts.APIHeapGap); err == nil {
			if r.cpRestartsAtStart == nil {
				r.cpRestartsAtStart = ManagerRestarts(cp)
			}
			cp = RestartsSince(r.cpRestartsAtStart, cp)
			if restarted := Restarted(cp); restarted != "" {
				described += " — " + restarted
			}
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
			// State, then what changed to get here. Every counter etcd keeps
			// is cumulative over a member's process life, so the state line's
			// means and sizes describe the store now and say nothing about
			// what this rung did to it — which is how "wal fsync 1.7ms" was
			// read as "etcd was never the problem" on a run whose managers
			// were dying to "etcdserver: request timed out". See EtcdSince.
			described := DescribeEtcdMembers(members)
			if since := EtcdSince(r.etcdBaseline, members); since != "" {
				described += " — " + since
			}
			report.AddFact("etcd@"+label, described)
			for name, member := range members {
				if member.NearQuota() {
					r.logf("WARNING at %s: %s %s", label, name, member.Describe())
				}
			}
			// Where the single points of failure sit, which decides what one
			// node's loss costs and which nothing else in the sample says.
			if placed := ReadPlacement(ctx, r.Host, store, members).Describe(); placed != "" {
				report.AddFact("placement@"+label, placed)
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

	// What the managers have already been through. A restart from a previous
	// run is not this run's ceiling, and reading the raw count made it one.
	// Pod status alone, as the death check reads it: the baseline it is
	// compared against should come from the same instrument.
	if components, err := r.Sampler.Health(ctx, r.Host, controllers); err == nil {
		r.managersAtStart = ManagerRestarts(components)
		if restarted := Classify(components, false); restarted != "" {
			report.AddFact("managerHistory", restarted)
			r.logf("NOTE: the managers carry history from before this run: %s", restarted)
		}
	} else {
		r.logf("NOTE: could not read the managers before the climb (%v), so a restart from a "+
			"previous run may be reported as this one's", err)
	}

	// What the control plane has already been through, before anything is
	// created. Every restart the health check reports from here is one this run
	// caused; without it, a kubeadm control plane pod restarted at any point in
	// the node's life would fail the first rung.
	if facts, beside, err := r.Sampler.ControlPlaneFacts(ctx, r.Host); err == nil {
		r.controlPlaneAtStart = facts
		r.besideAtStart = beside
		if restarted := Classify(HealthOf(facts), false); restarted != "" {
			report.AddFact("controlPlaneHistory", restarted)
			r.logf("NOTE: the control plane carries history from before this run: %s", restarted)
		}
	} else {
		r.logf("NOTE: could not read the control plane's pods (%v), so a process dying during the "+
			"run will not be noticed as one", err)
	}

	r.store = store
	r.defragment(ctx, report, store, "baseline")

	// The baseline, before any fleet exists. Every slope this run reports is a
	// difference between two large numbers, and without this the smaller of
	// them is still a fleet.
	sample("baseline (no clusters)", 0, 0)

	// And whether that baseline is this run's. A process that was already
	// running holds whatever it served last, which on one measured cluster was
	// twenty-three times the fleet's own cost. See Inherited.
	if len(report.Samples) > 0 {
		base := report.Samples[len(report.Samples)-1]
		// By age where a process publishes one, and by size for the API
		// servers, which do not. See InheritedControlPlane.
		old := append(Inherited(base.Components, started), InheritedControlPlane(base.Components)...)
		if note := DescribeInherited(old); note != "" {
			report.AddFact("inheritedBaseline", note)
			r.logf("WARNING: %s", note)
		}
	}

	var rungs []RungResult
	held := 0
	// The tenants of the fleet that converged, and of the rung that did not.
	// Create returns every tenant a rung's fleet has, the ones already there
	// included, so the difference between the two is exactly what the failed
	// rung added. See soak.
	var heldTenants, failedTenants []string
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
			failure = annotate(failure, r.beside(ctx), r.strain(ctx))
			rungs = append(rungs, RungResult{
				Clusters: clusters, Machines: machines, Added: clusters - held,
				CreatedIn: time.Since(startedCreate),
				Failure:   failure,
			})
			failedTenants = madeTenants
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
			failedTenants = madeTenants
		} else {
			heldTenants = madeTenants
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
		r.trimToHeld(ctx, report, heldTenants, failedTenants)
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
//
// Then it retakes the store's baseline, whether or not the defragmentation
// ran, so that the rewrite it just did is not counted as strain the next rung
// caused. Every rung's strain line is therefore measured from the
// defragmentation before it — see EtcdSince.
func (r *Runner) defragment(ctx context.Context, report *deployedscale.Report, store StoreLocation, at string) {
	results, err := r.Defragmenter.AllAt(ctx, r.Host, r.Sampler, store)
	if err != nil {
		r.logf("NOTE: could not defragment before %s: %v", at, err)
	} else {
		report.AddFact("defrag@"+at, DescribeDefrag(results))
		r.logf("%s", DescribeDefrag(results))
	}

	if members, err := r.Sampler.EveryEtcdMember(ctx, r.Host, store); err == nil {
		r.etcdBaseline = members
	} else if r.etcdBaseline == nil {
		r.logf("NOTE: could not read etcd's counters (%v), so a store that stalls under the "+
			"fleet will not be reported as the reason", err)
	} else {
		r.logf("NOTE: could not retake etcd's baseline before %s (%v), so the next strain line "+
			"is measured from the defragmentation before this one", at, err)
	}
}

// trimToHeld removes what the failed rung added, so that the soak holds the
// fleet it is labelled with.
//
// Rungs are cumulative: a rung keeps the fleet below it and adds to it, so when
// the 2500 rung fails the cluster is holding 2500 clusters, and a soak that
// followed labelled them 2000. Every drift figure and the readiness count at
// the end were then about a fleet nobody had measured — partly converged, and
// larger than the one the report said it held.
//
// Only the failed rung's own tenants go: the difference between what its
// Create returned and what the last converged rung's did. A teardown that does
// not finish is recorded and the soak still runs, because a soak with a caveat
// is worth more than none and the caveat is on the line.
func (r *Runner) trimToHeld(ctx context.Context, report *deployedscale.Report, held, failed []string) {
	extra := notIn(failed, held)
	if len(extra) == 0 {
		return
	}
	r.logf("=== removing the %d tenants the failed rung added before the soak", len(extra))
	if err := r.Target.Teardown(ctx, extra, r.Options.TeardownTimeout, r.Options.PollInterval, r.Logf); err != nil {
		report.AddFact("soakFleet", fmt.Sprintf("the failed rung's %d tenants could not all be removed "+
			"before the soak (%v), so the soak holds more than the fleet it is labelled with",
			len(extra), err))
		r.logf("NOTE: %s", report.Facts["soakFleet"])
		return
	}
	r.Created = notIn(r.Created, extra)
	report.AddFact("soakFleet", fmt.Sprintf("the failed rung's %d tenants were removed before the "+
		"soak, so the soak holds the last fleet that converged and nothing above it", len(extra)))
}

// notIn is the names in all that are not in some, in the order all has them.
func notIn(all, some []string) []string {
	skip := make(map[string]bool, len(some))
	for _, name := range some {
		skip[name] = true
	}
	var out []string
	for _, name := range all {
		if !skip[name] {
			out = append(out, name)
		}
	}
	return out
}

// died reports the first component that has stopped since this run began, or
// "" when everything is still up.
//
// The managers and the control plane both, because a run aimed at a ceiling has
// to be able to say the API server was OOM killed rather than that
// reconciliation stopped keeping up. Cheap by construction: both halves read
// pod status and no metrics, and the managers are profiled once, only when
// one of them has died, for the throttling figure that says whether it was
// short of CPU. See Sampler.Health for what the profiling on every poll cost.
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
	if health, err := r.Sampler.Health(ctx, r.Host, controllers); err == nil {
		// Against the baseline, exactly as the control plane is. A manager
		// that died in a previous run otherwise fails every rung of every run
		// afterwards, for ever. See ManagersSince.
		since := ManagersSince(r.managersAtStart, health)
		if why := Classify(since, false); why != "" {
			// With the kernel's own accounting for the component that died.
			// A manager killed while starved of quota and one killed with CPU
			// to spare are different findings, and a sample carries the
			// difference — died() used to discard it, so establishing which
			// was a scrape by hand after the run was over. One sample, now
			// that there is a death to explain.
			if _, throttling, err := r.Sampler.Sample(ctx, r.Host, controllers); err == nil {
				if th, ok := throttling[Culprit(since)]; ok && th.Periods > 0 {
					why += " — " + th.Describe()
					if !th.Significant() {
						why += ", so it was not short of CPU"
					}
				}
			}
			return why
		}
	}
	if facts, _, err := r.Sampler.ControlPlaneFacts(ctx, r.Host); err == nil {
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
// beside reports a death among the pods that run on the control plane's nodes
// without being the control plane, or "" when there was none.
//
// A note rather than a verdict. The cloud controller manager that ended a rung
// at 1500 clusters lost its lease on a PUT with a five-second timeout — a
// tighter deadline than anything in Cluster API sets, which makes it the first
// thing to notice an API server that has stopped answering. That is worth
// having in the failure line and it is not the ceiling: it is not the system
// under test, and a run that stops when it dies is reporting the fuse length of
// the most impatient neighbour.
func (r *Runner) beside(ctx context.Context) string {
	if r.besideAtStart == nil {
		return ""
	}
	_, beside, err := r.Sampler.ControlPlaneFacts(ctx, r.Host)
	if err != nil {
		return ""
	}
	if why := Classify(HealthSince(r.besideAtStart, beside), false); why != "" {
		return "beside the control plane, " + why
	}
	return ""
}

func (r *Runner) strain(ctx context.Context) string {
	if r.etcdBaseline == nil {
		return ""
	}
	members, err := r.Sampler.EveryEtcdMember(ctx, r.Host, r.store)
	if err != nil {
		return ""
	}
	return EtcdSince(r.etcdBaseline, members)
}

// annotate appends every note that has something to say to a failure line,
// and leaves the line alone when none does.
//
// One place rather than three, because the three failure paths — a rung that
// could not be created, a process that died, a fleet that did not arrive —
// each grew this by hand, and the death path was the one that missed the
// store's counters until a run needed them there.
func annotate(why string, notes ...string) string {
	for _, note := range notes {
		if note != "" {
			why += " — " + note
		}
	}
	return why
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
		steady.Observe(last)

		// A component that died is why the fleet has not arrived, rather than
		// a second thing that went wrong. Checked every poll so that a kill is
		// reported when it happens rather than after the step timeout.
		//
		// Before the count is believed, not after. A fleet can arrive over a
		// dead process — the managers that are left finish the work — and the
		// 2000-cluster rung did exactly that: the bootstrap manager died
		// eleven minutes before the count reached its target, the wait
		// returned on the count without looking, and the death surfaced as
		// the next rung's failure thirty-one seconds in, charged to a rung
		// that had barely started while the one that killed it stood as a
		// success.
		//
		// With what the store was doing, the same as a timeout carries. A
		// process that dies under load usually died of the store — a lease it
		// could not renew — and the one time that was the whole story, the
		// death path was the only failure path that left the store's counters
		// off the line.
		if why := r.died(ctx, controllers); why != "" {
			return false, annotate(why, r.beside(ctx), r.strain(ctx))
		}
		if last.Done {
			return true, ""
		}

		if time.Now().After(deadline) {
			// A fleet that never arrived, a fleet that arrived and would not
			// hold still, and a fleet that all but arrived and stopped are
			// three findings, and the last poll's count cannot tell them
			// apart. See Steadiness. With the names of what is not ready,
			// because the fleet is torn down at the end of the run and the
			// evidence goes with it.
			why := fmt.Sprintf("%s (%s)", timedOutBecause(steady), last.Describe())
			return false, annotate(why, last.DescribeStragglers(), steady.Describe(), r.beside(ctx), r.strain(ctx))
		}
		r.logf("    %s", last.Describe())
		// Named while the rung is still waiting, so that a person can go and
		// look at the straggler before the step timeout takes it away. On the
		// poll the fleet is first judged stuck, and every so often after.
		if steady.Stuck() && (steady.Motionless+1-stuckPolls)%stuckPolls == 0 {
			r.logf("    stuck: %s", last.DescribeStragglers())
		}
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
