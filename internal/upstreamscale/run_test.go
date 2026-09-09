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
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// fakeTarget is a side of the comparison with no cluster behind it, which is
// what lets the run loop itself be tested — it never was, being reachable only
// through an integration test that needs a real cluster and forty minutes.
type fakeTarget struct {
	name   string
	tenant string

	// failAt is the cluster count whose rung never converges, zero for none.
	failAt int
	// createErrAt is the cluster count whose creation half-fails.
	createErrAt int
	// dieAt is the cluster count at which a control plane pod on host is
	// found restarted at the same moment the fleet arrives, zero for never.
	dieAt int
	host  client.Client
	// shortAt is the cluster count whose rung arrives one cluster short and
	// stays there, with the straggler named.
	shortAt int
	// managers is what Controllers returns: a manager on host whose pod the
	// death check reads, when the test has put one there.
	managers []Controller
	// killManagerAt is the cluster count at which that manager's pod is found
	// restarted, zero for never.
	killManagerAt int
	// sidecarDiesAt is the cluster count at which kube-vip and the cloud
	// controller manager on host are found restarted, zero for never.
	sidecarDiesAt int

	created  []string
	planned  []int
	tornDown []string
}

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Title(startClusters, nodes int) string {
	return fmt.Sprintf("%s: climbing from %d clusters at %d nodes each", f.name, startClusters, nodes)
}

func (f *fakeTarget) Facts() map[string]string {
	return map[string]string{"tenancy": f.tenant, "side": f.name}
}

func (f *fakeTarget) Prepare(context.Context) error { return nil }
func (f *fakeTarget) Controllers() []Controller     { return f.managers }
func (f *fakeTarget) Store() StoreLocation          { return StoreLocation{Namespace: "nowhere"} }

func (f *fakeTarget) ControlPlane(context.Context, client.Client, int, time.Duration,
) ([]deployedscale.ComponentSample, string, error) {
	return []deployedscale.ComponentSample{{Component: f.name + "-control-plane"}}, "a control plane", nil
}

func (f *fakeTarget) Plan(clusters int) (Fleet, error) {
	f.planned = append(f.planned, clusters)
	return PlanFleet(FleetShape{
		Clusters: clusters, ClustersPerNamespace: 10, ControlPlaneMachines: 1,
	}), nil
}

func (f *fakeTarget) Create(_ context.Context, fleet Fleet, _ int) ([]string, error) {
	var made []string
	for _, ns := range fleet.Namespaces {
		made = append(made, ns.Name)
	}
	f.created = append(f.created, made...)
	if f.createErrAt != 0 && fleet.Shape.Clusters == f.createErrAt {
		// Half built: some tenants exist and the rung failed, which is the
		// case teardown must still cover.
		return made, errors.New("the infrastructure said no")
	}
	return made, nil
}

func (f *fakeTarget) Converged(ctx context.Context, wantClusters, wantMachines int) (Convergence, error) {
	if f.dieAt != 0 && wantClusters == f.dieAt {
		// The fleet arrives and the API server has already died: the count
		// says Done and the pod says restarted, in the same poll.
		var pod corev1.Pod
		if err := f.host.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-apiserver-cp-0"}, &pod); err != nil {
			return Convergence{}, err
		}
		pod.Status.ContainerStatuses[0].RestartCount = 1
		pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "Error"},
		}
		if err := f.host.Status().Update(ctx, &pod); err != nil {
			return Convergence{}, err
		}
	}
	if f.killManagerAt != 0 && wantClusters == f.killManagerAt {
		var pod corev1.Pod
		if err := f.host.Get(ctx, client.ObjectKey{Namespace: "capi-system", Name: "capi-controller-manager-abc"}, &pod); err != nil {
			return Convergence{}, err
		}
		pod.Status.ContainerStatuses[0].RestartCount = 1
		pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"},
		}
		if err := f.host.Status().Update(ctx, &pod); err != nil {
			return Convergence{}, err
		}
	}
	if f.sidecarDiesAt != 0 && wantClusters == f.sidecarDiesAt {
		// kube-vip exits 0 when it loses its lease; the CCM exits 1.
		for name, exit := range map[string]int32{"kube-vip-cp-0": 0, "nutanix-cloud-controller-manager-abc": 1} {
			var pod corev1.Pod
			if err := f.host.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: name}, &pod); err != nil {
				return Convergence{}, err
			}
			pod.Status.ContainerStatuses[0].RestartCount = 1
			pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Reason: "Error"},
			}
			if err := f.host.Status().Update(ctx, &pod); err != nil {
				return Convergence{}, err
			}
		}
	}
	if f.failAt != 0 && wantClusters == f.failAt {
		return Convergence{ControlPlanesWant: wantClusters, MachinesWant: wantMachines}, nil
	}
	if f.shortAt != 0 && wantClusters == f.shortAt {
		return Convergence{
			ControlPlanesReady: wantClusters - 1, ControlPlanesWant: wantClusters,
			MachinesReady: wantMachines - 1, MachinesWant: wantMachines,
			Stragglers: []string{"capi-scale-0003/c0039: control plane 2 of 3 ready, Available=False (NotAvailable: Etcd member 1 does not have a corresponding Machine)"},
		}, nil
	}
	return Convergence{
		ControlPlanesReady: wantClusters, ControlPlanesWant: wantClusters,
		MachinesReady: wantMachines, MachinesWant: wantMachines,
		Done: true,
	}, nil
}

func (f *fakeTarget) Teardown(_ context.Context, created []string, _, _ time.Duration,
	_ func(string, ...any),
) error {
	f.tornDown = append(f.tornDown, created...)
	return nil
}

func testRunner(t *testing.T, target Target, start, max int, host ...client.Object) *Runner {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return &Runner{
		Target:       target,
		Host:         fake.NewClientBuilder().WithScheme(s).WithObjects(host...).Build(),
		Sampler:      &Sampler{},
		Defragmenter: NewDefragmenter(nil, nil),
		Options: RunOptions{
			StartClusters: start, MaxClusters: max, NodesPerCluster: 1,
			CreateConcurrency: 4,
			SettleTolerance:   0.02,
			SettleTimeout:     time.Millisecond,
			StepTimeout:       10 * time.Millisecond,
			PollInterval:      time.Millisecond,
			TeardownTimeout:   time.Second,
		},
	}
}

// TestTheClimbStopsAtTheFirstRungThatDoesNotConverge, and does not go on to
// build the one above it. A ladder that kept climbing past a failure would be
// measuring a cluster that had already given up.
func TestTheClimbStopsAtTheFirstRungThatDoesNotConverge(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", failAt: 4}
	runner := testRunner(t, target, 2, 8)

	report, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("a climb that reached a rung is not an error: %v", err)
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 2 {
		t.Fatalf("last good rung = %+v, want 2 clusters", ceiling.LastGood)
	}
	if ceiling.Failed == nil || ceiling.Failed.Clusters != 4 {
		t.Fatalf("failed rung = %+v, want 4 clusters", ceiling.Failed)
	}
	// The rung above the failure was never planned, let alone created.
	for _, planned := range target.planned {
		if planned == 8 {
			t.Error("the climb went on past a rung that did not converge")
		}
	}
	if d := ceiling.Describe(); !strings.Contains(d, "did not") {
		t.Errorf("the ceiling sentence does not name the failure: %q", d)
	}
	// And the report says what happened at both rungs.
	if _, ok := report.Facts["rung@2"]; !ok {
		t.Error("no timing for the rung that converged")
	}
	if _, ok := report.Facts["rung@4"]; !ok {
		t.Error("no timing for the rung that did not")
	}
}

// TestAHalfBuiltRungIsStillTornDown. Create returns what it made even when it
// fails, because the alternative is a rung's worth of tenants left on a cluster
// whose next run would measure them as its baseline.
func TestAHalfBuiltRungIsStillTornDown(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", createErrAt: 2}
	runner := testRunner(t, target, 2, 2)

	if _, _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("a run whose only rung could not be created reported success")
	}
	if len(runner.Created) == 0 {
		t.Fatal("the runner recorded nothing to tear down")
	}
	if err := runner.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(target.tornDown) != len(runner.Created) {
		t.Errorf("tore down %d of %d tenants", len(target.tornDown), len(runner.Created))
	}
}

// TestARunThatMeasuredNothingIsAnError, while one that found a ceiling is a
// result whichever rung it stopped at. That distinction is what the whole
// harness is built around, so it is checked rather than assumed.
func TestARunThatMeasuredNothingIsAnError(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", failAt: 2}
	runner := testRunner(t, target, 2, 4)

	report, ceiling, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("a climb whose first rung failed reported success")
	}
	if ceiling.LastGood != nil {
		t.Errorf("something was reported as measured: %+v", ceiling.LastGood)
	}
	// The report still exists, because a failed climb's samples are how
	// anybody works out why.
	if report == nil || len(report.Samples) == 0 {
		t.Error("a failed climb produced no samples at all")
	}
}

// TestBothSidesProduceTheSameShapeOfReport.
//
// This is the comparison, in code. Two targets differing in everything a
// Target is allowed to differ in — the name, the tenancy unit, what the control
// plane is called — climbed by the same Runner must produce reports whose facts
// line up, or a reader diffing two runs is reading two different instruments
// again, which is the thing this refactor exists to stop.
func TestBothSidesProduceTheSameShapeOfReport(t *testing.T) {
	stock := &fakeTarget{name: "stock", tenant: "Namespace"}
	kcp := &fakeTarget{name: "kcp", tenant: "Workspace"}

	stockReport, _, err := testRunner(t, stock, 2, 4).Run(context.Background())
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	kcpReport, _, err := testRunner(t, kcp, 2, 4).Run(context.Background())
	if err != nil {
		t.Fatalf("kcp: %v", err)
	}

	for key := range stockReport.Facts {
		if _, ok := kcpReport.Facts[key]; !ok {
			t.Errorf("the stock report has %q and the kcp one does not", key)
		}
	}
	for key := range kcpReport.Facts {
		if _, ok := stockReport.Facts[key]; !ok {
			t.Errorf("the kcp report has %q and the stock one does not", key)
		}
	}

	if len(stockReport.Samples) != len(kcpReport.Samples) {
		t.Errorf("%d samples against %d: the two sides were not asked the same questions",
			len(stockReport.Samples), len(kcpReport.Samples))
	}
	for i := range stockReport.Samples {
		if a, b := stockReport.Samples[i].Label, kcpReport.Samples[i].Label; a != b {
			t.Errorf("sample %d is %q on one side and %q on the other", i, a, b)
		}
	}

	// And each still says which side it is, or two comparable reports are
	// indistinguishable.
	if stockReport.Facts["side"] == kcpReport.Facts["side"] {
		t.Error("the two reports do not say which side they are")
	}
}

// TestTheBaselineIsSampledBeforeAnythingIsCreated, because every slope a run
// reports is a difference against it.
func TestTheBaselineIsSampledBeforeAnythingIsCreated(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace"}
	runner := testRunner(t, target, 2, 2)

	report, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(report.Samples) == 0 {
		t.Fatal("no samples")
	}
	first := report.Samples[0]
	if first.Clusters != 0 || !strings.Contains(first.Label, "baseline") {
		t.Errorf("the first sample is %q with %d clusters, want an empty baseline",
			first.Label, first.Clusters)
	}
}

// TestAFailureLineCarriesEveryNoteThatHasSomethingToSay, and none that does
// not.
//
// The run this is from: a rung ended on a process that died, and the line said
// only that it died. The store's counters for the same minute — the failed
// proposals that were the actual cause — were attached to the timeout path
// and not to the death path, so the one time they would have explained a
// failure they were not on it.
func TestAFailureLineCarriesEveryNoteThatHasSomethingToSay(t *testing.T) {
	got := annotate("kube-vip restarted 1 time(s)", "", "beside the control plane, nothing",
		"", "etcd, since its last defragmentation — etcd-2: 1 failed raft proposal(s)")
	want := "kube-vip restarted 1 time(s) — beside the control plane, nothing — " +
		"etcd, since its last defragmentation — etcd-2: 1 failed raft proposal(s)"
	if got != want {
		t.Errorf("annotate() = %q, want %q", got, want)
	}
	if got := annotate("the fleet did not arrive"); got != "the fleet did not arrive" {
		t.Errorf("a line with nothing to add was changed: %q", got)
	}
}

// controlPlaneOnHost is a one-node kubeadm control plane as the host cluster
// sees it: a labelled node and a static API server pod that has never
// restarted.
func controlPlaneOnHost() []client.Object {
	return []client.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "cp-0", Labels: map[string]string{ControlPlaneNodeLabel: ""},
		}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kube-apiserver-cp-0", Namespace: "kube-system",
				Annotations: map[string]string{deployedscale.MirrorPodAnnotation: "x"},
			},
			Spec: corev1.PodSpec{
				NodeName:   "cp-0",
				Containers: []corev1.Container{{Name: "kube-apiserver"}},
			},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "kube-apiserver", Ready: true}},
			},
		},
	}
}

// TestAFleetThatArrivedOverADeadProcessDidNotConverge.
//
// The run this is from: the 2000-cluster rung was declared converged, and the
// kubeadm bootstrap manager had died inside it, eleven minutes before the
// count reached its target. The wait returned on the count before it looked
// at the pods, so the death surfaced as the next rung's failure after
// thirty-one seconds — charged to a rung that had barely started, with the
// rung that actually killed it recorded as a success.
func TestAFleetThatArrivedOverADeadProcessDidNotConverge(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", dieAt: 4}
	runner := testRunner(t, target, 2, 8, controlPlaneOnHost()...)
	target.host = runner.Host

	_, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("a climb that reached a rung is not an error: %v", err)
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 2 {
		t.Fatalf("last good rung = %+v, want 2 clusters", ceiling.LastGood)
	}
	if ceiling.Failed == nil || ceiling.Failed.Clusters != 4 {
		t.Fatalf("failed rung = %+v, want the rung the process died in", ceiling.Failed)
	}
	if !strings.Contains(ceiling.Failed.Failure, "kube-apiserver-cp-0 restarted") {
		t.Errorf("the failure does not name the process that died: %q", ceiling.Failed.Failure)
	}
	for _, planned := range target.planned {
		if planned == 8 {
			t.Error("the climb went on past the rung whose process died")
		}
	}
}

// TestTheFailedRungIsRemovedBeforeTheSoak.
//
// Rungs are cumulative, so when the 2500 rung fails the cluster is holding
// 2500 clusters, and the soak that follows labels them 2000. Every drift
// figure and the readiness count at the end are then about the wrong fleet.
// The tenants the failed rung added — and only those — are torn down first.
func TestTheFailedRungIsRemovedBeforeTheSoak(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", failAt: 40}
	runner := testRunner(t, target, 10, 40)
	runner.Options.Soak = 3 * time.Millisecond
	runner.Options.SoakInterval = time.Millisecond

	report, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 20 {
		t.Fatalf("last good rung = %+v, want 20 clusters", ceiling.LastGood)
	}

	// Ten clusters to a namespace: rung 20 is 0000 and 0001, rung 40 added
	// 0002 and 0003, and those two are what went before the soak.
	want := []string{NamespaceName(2), NamespaceName(3)}
	if strings.Join(target.tornDown, ",") != strings.Join(want, ",") {
		t.Errorf("torn down before the soak: %v, want %v", target.tornDown, want)
	}
	for _, name := range runner.Created {
		if name == NamespaceName(2) || name == NamespaceName(3) {
			t.Errorf("%s was torn down and is still recorded as created", name)
		}
	}
	for _, name := range []string{NamespaceName(0), NamespaceName(1)} {
		if !slices.Contains(runner.Created, name) {
			t.Errorf("%s is part of the held fleet and was forgotten", name)
		}
	}
	fact, ok := report.Facts["soakFleet"]
	if !ok || !strings.Contains(fact, "2 tenants") {
		t.Errorf("the report does not say the soak's fleet was trimmed: %q", fact)
	}
}

// TestASoakAfterACleanClimbRemovesNothing: a climb that never failed has
// nothing above its last good rung to remove.
func TestASoakAfterACleanClimbRemovesNothing(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace"}
	runner := testRunner(t, target, 10, 20)
	runner.Options.Soak = 3 * time.Millisecond
	runner.Options.SoakInterval = time.Millisecond

	report, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(target.tornDown) != 0 {
		t.Errorf("a clean climb tore down %v before its soak", target.tornDown)
	}
	if _, ok := report.Facts["soakFleet"]; ok {
		t.Error("a soak of the whole fleet claims to have trimmed it")
	}
}

// TestAStuckRungNamesWhatItIsStuckOn, in the failure line, because the fleet
// is torn down at the end of the run and the evidence goes with it.
func TestAStuckRungNamesWhatItIsStuckOn(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", shortAt: 40}
	runner := testRunner(t, target, 10, 40)
	runner.Options.StepTimeout = 200 * time.Millisecond
	var logged []string
	runner.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	_, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ceiling.Failed == nil || ceiling.Failed.Clusters != 40 {
		t.Fatalf("failed rung = %+v, want 40", ceiling.Failed)
	}
	if !strings.Contains(ceiling.Failed.Failure, "stuck") {
		t.Errorf("a rung one short and motionless is not called stuck: %q", ceiling.Failed.Failure)
	}
	if !strings.Contains(ceiling.Failed.Failure, "capi-scale-0003/c0039") {
		t.Errorf("the failure line does not name the straggler: %q", ceiling.Failed.Failure)
	}
	// And while it was still waiting, so that a person can look before the
	// step timeout takes the fleet away.
	if !slices.ContainsFunc(logged, func(line string) bool {
		return strings.Contains(line, "stuck") && strings.Contains(line, "capi-scale-0003/c0039")
	}) {
		t.Error("the straggler was not logged while the rung was still waiting")
	}
}

// managerOnHost is one manager as the host cluster sees it: a Deployment and
// its one running pod, never restarted.
func managerOnHost() ([]client.Object, []Controller) {
	labels := map[string]string{"control-plane": "controller-manager"}
	objects := []client.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "capi-controller-manager", Namespace: "capi-system"},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "capi-controller-manager-abc", Namespace: "capi-system", Labels: labels},
			Spec:       corev1.PodSpec{NodeName: "md-0-a", Containers: []corev1.Container{{Name: "manager"}}},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", Ready: true}},
			},
		},
	}
	return objects, []Controller{{
		Name: "capi", Namespace: "capi-system", Deployment: "capi-controller-manager", Container: "manager",
	}}
}

// TestTheDeathCheckReadsNoProfiles.
//
// The poll asked whether anything had died by taking a full sample of every
// manager, and a sample reads a heap profile with a forced collection. At
// 1600 clusters that was a full collection of a multi-gigabyte heap in four
// processes, four times a minute, for the length of every rung — charged to
// the rung whose verdict was "reconciliation did not keep up", and carried
// through the VIP to the API server whose etcd member kept stalling. Whether
// a process died is in its pod status, which costs the API server a list.
func TestTheDeathCheckReadsNoProfiles(t *testing.T) {
	objects, managers := managerOnHost()
	// A rung that sits one short for the whole step timeout, so the wait
	// polls many times; every poll used to profile.
	target := &fakeTarget{name: "stock", tenant: "Namespace", managers: managers, shortAt: 4}
	runner := testRunner(t, target, 2, 4, objects...)
	target.host = runner.Host
	runner.Options.StepTimeout = 100 * time.Millisecond

	if _, _, err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The settle, the baseline and each rung's sample may profile; a hundred
	// polls of the wait must not.
	if got := runner.Sampler.profileReads; got > 6 {
		t.Errorf("%d profiles were read across a climb of two rungs; the death check is profiling", got)
	}
}

// TestADeathIsProfiledOnce, so the failure line still says whether the
// process that died was short of CPU — the sample is what carries the
// kernel's throttling figure, and it is worth one read once there is
// something to explain.
func TestADeathIsProfiledOnce(t *testing.T) {
	reads := func(killAt int) int {
		objects, managers := managerOnHost()
		target := &fakeTarget{name: "stock", tenant: "Namespace", managers: managers, killManagerAt: killAt}
		runner := testRunner(t, target, 2, 4, objects...)
		target.host = runner.Host
		_, ceiling, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if killAt != 0 && (ceiling.Failed == nil || !strings.Contains(ceiling.Failed.Failure, "capi-controller-manager restarted")) {
			t.Fatalf("the manager's death was not the failure: %+v", ceiling.Failed)
		}
		return runner.Sampler.profileReads
	}
	clean, dead := reads(0), reads(4)
	if dead != clean+1 {
		t.Errorf("a climb with a death read %d profiles against %d without one, want exactly one more", dead, clean)
	}
}

// sidecarsOnHost is what stands beside a kubeadm control plane on its node:
// kube-vip as a static pod, and a cloud controller manager scheduled there.
func sidecarsOnHost() []client.Object {
	return []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kube-vip-cp-0", Namespace: "kube-system",
				Annotations: map[string]string{deployedscale.MirrorPodAnnotation: "x"},
			},
			Spec: corev1.PodSpec{NodeName: "cp-0", Containers: []corev1.Container{{Name: "kube-vip"}}},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "kube-vip", Ready: true}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "nutanix-cloud-controller-manager-abc", Namespace: "kube-system"},
			Spec:       corev1.PodSpec{NodeName: "cp-0", Containers: []corev1.Container{{Name: "manager"}}},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", Ready: true}},
			},
		},
	}
}

// TestASidecarDeathIsAnIncidentNotACeiling.
//
// Four runs ended their top rung on kube-vip losing its lease, nine and a half
// minutes into a rung whose predecessor took ten and three quarters to
// converge, so none of them learned whether the fleet would have arrived. The
// rung now runs on and records the death where it happened; the climb goes on
// above it; and the ceiling names the clean fleet as the one to recommend and
// the reached fleet as reached.
func TestASidecarDeathIsAnIncidentNotACeiling(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", sidecarDiesAt: 4}
	runner := testRunner(t, target, 2, 8, append(controlPlaneOnHost(), sidecarsOnHost()...)...)
	target.host = runner.Host

	report, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("a climb that reached a rung is not an error: %v", err)
	}
	if ceiling.Failed != nil {
		t.Fatalf("a sidecar's death ended the climb: %+v", ceiling.Failed)
	}
	if !slices.Contains(target.planned, 8) {
		t.Error("the climb did not go on above the rung whose sidecar died")
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 8 {
		t.Fatalf("last good rung = %+v, want 8 clusters", ceiling.LastGood)
	}
	if ceiling.LastClean == nil || ceiling.LastClean.Clusters != 2 {
		t.Fatalf("last clean rung = %+v, want 2 clusters, below the rung the VIP moved in", ceiling.LastClean)
	}
	if len(ceiling.Unclean) != 1 || ceiling.Unclean[0].Clusters != 4 {
		t.Fatalf("unclean rungs = %+v, want the one at 4 clusters", ceiling.Unclean)
	}

	incidents := strings.Join(ceiling.Unclean[0].Incidents, "\n")
	for _, want := range []string{"kube-vip-cp-0 restarted 1 time(s)", KubeVIPLease, "endpoint moved",
		"nutanix-cloud-controller-manager-abc restarted 1 time(s)", "leader election"} {
		if !strings.Contains(incidents, want) {
			t.Errorf("the rung's incidents do not say %q:\n%s", want, incidents)
		}
	}
	if strings.Contains(incidents, "not comparable") {
		t.Errorf("a sidecar was described as a manager whose samples broke: %s", incidents)
	}
	// Charged once: the rung above still sees the same restart counts and
	// must not report them again.
	if got := ceiling.LastGood.Incidents; len(got) != 0 {
		t.Errorf("the rung above repeated the incidents below it: %v", got)
	}

	if fact := report.Facts["rung@4"]; !strings.Contains(fact, "not cleanly") || !strings.Contains(fact, "kube-vip") {
		t.Errorf("the rung's line does not carry its incident: %q", fact)
	}
	if fact := report.Facts["rung@8"]; strings.Contains(fact, "kube-vip") {
		t.Errorf("the clean rung above carries the incident: %q", fact)
	}
	described := report.Facts["ceiling"]
	for _, want := range []string{"Held 2 clusters", "8 clusters and 8 Machines converged", "not one to recommend",
		"floor, not a ceiling"} {
		if !strings.Contains(described, want) {
			t.Errorf("the ceiling does not say %q: %s", want, described)
		}
	}
}

// TestALadderPastTheClusterIsWarnedAboutBeforeAnythingIsCreated. A climb that
// stops at 2000 on a node the measured runs put at 1500 costs a day; the model
// says so at the start, as a warning and a fact, and the run goes on to find
// out.
func TestALadderPastTheClusterIsWarnedAboutBeforeAnythingIsCreated(t *testing.T) {
	small := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-small", Labels: map[string]string{ControlPlaneNodeLabel: ""}},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("14Gi")}},
	}
	target := &fakeTarget{name: "stock", tenant: "Namespace"}
	runner := testRunner(t, target, 2, 8, small)
	target.host = runner.Host
	measured := MeasuredCapacity()
	runner.Options.Capacity = &measured
	var logged []string
	runner.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	report, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	fact := report.Facts["capacity"]
	if !strings.Contains(fact, "cp-small") || !strings.Contains(fact, "past that") {
		t.Errorf("the capacity fact does not say the ladder is past the node: %q", fact)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "WARNING: the ladder asks for more") }) {
		t.Errorf("no warning was logged before the climb: %q", logged)
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 8 {
		t.Errorf("the warning stopped the climb: %+v", ceiling)
	}

	// Without the model, nothing is said.
	quiet := &fakeTarget{name: "stock", tenant: "Namespace"}
	plain := testRunner(t, quiet, 2, 4, small)
	quiet.host = plain.Host
	report, _, err = plain.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := report.Facts["capacity"]; ok {
		t.Error("a run with no model carried a capacity fact")
	}
}
