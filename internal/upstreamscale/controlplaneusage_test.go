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

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func usageOf(ws uint64, cpu float64) ContainerUsage {
	return ContainerUsage{WorkingSetBytes: ws, CPUSeconds: cpu}
}

// TestTheControlPlaneIsEveryProcessOnItsNodes.
//
// "What does the control plane cost" is a question about the machines, not
// about the one component somebody scraped. Three API servers, three etcd
// members, the controller manager and the scheduler all run on those nodes and
// all of them are the control plane's cost.
func TestTheControlPlaneIsEveryProcessOnItsNodes(t *testing.T) {
	usage := map[string]PodUsage{
		"kube-system/kube-apiserver-cp-0":          {ContainerUsage: usageOf(21_288_392_704, 14204.5), Role: "kube-apiserver", Node: "cp-0"},
		"kube-system/kube-apiserver-cp-1":          {ContainerUsage: usageOf(20_401_094_656, 13980.0), Role: "kube-apiserver", Node: "cp-1"},
		"kube-system/etcd-cp-0":                    {ContainerUsage: usageOf(2_684_354_560, 3810.25), Role: "etcd", Node: "cp-0"},
		"kube-system/kube-controller-manager-cp-0": {ContainerUsage: usageOf(5_368_709_120, 6120.75), Role: "kube-controller-manager", Node: "cp-0"},
		"kube-system/kube-scheduler-cp-0":          {ContainerUsage: usageOf(134_217_728, 210.5), Role: "kube-scheduler", Node: "cp-0"},
	}
	samples := ControlPlaneUsage(usage, nil)

	if len(samples) != 5 {
		t.Fatalf("%d components, want every pod on the nodes", len(samples))
	}
	byName := map[string]deployedscale.ComponentSample{}
	for _, s := range samples {
		byName[s.Component] = s
	}
	for _, want := range []string{
		"kube-apiserver-cp-0", "kube-apiserver-cp-1", "etcd-cp-0",
		"kube-controller-manager-cp-0", "kube-scheduler-cp-0",
	} {
		if _, ok := byName[want]; !ok {
			t.Errorf("%s is not in the report: %v", want, byName)
		}
	}
	api := byName["kube-apiserver-cp-0"]
	if api.Process.ResidentBytes != 21_288_392_704 || api.Process.CPUSeconds != 14204.5 {
		t.Errorf("the API server's figures did not survive: %+v", api.Process)
	}
}

// TestTheComponentsComeBackInAStableOrder, so a report's rows are the same from
// one sample to the next and a diff of two runs is a diff of numbers.
func TestTheComponentsComeBackInAStableOrder(t *testing.T) {
	usage := map[string]PodUsage{
		"kube-system/kube-scheduler-cp-2": {ContainerUsage: usageOf(1, 1), Role: "kube-scheduler", Node: "cp-2"},
		"kube-system/kube-apiserver-cp-0": {ContainerUsage: usageOf(2, 2), Role: "kube-apiserver", Node: "cp-0"},
		"kube-system/etcd-cp-1":           {ContainerUsage: usageOf(3, 3), Role: "etcd", Node: "cp-1"},
	}
	first := ControlPlaneUsage(usage, nil)
	for range 5 {
		again := ControlPlaneUsage(usage, nil)
		for i := range first {
			if first[i].Component != again[i].Component {
				t.Fatalf("order moved: %v then %v", componentNames(first), componentNames(again))
			}
		}
	}
	if first[0].Component != "etcd-cp-1" {
		t.Errorf("components = %v, want them sorted", componentNames(first))
	}
}

// TestARestartedControlPlaneProcessSaysSo.
//
// This is the one that decides whether a run aimed at a ceiling can report what
// it found. The control plane's samples carried invented pod facts — always
// ready, never restarted — so an API server killed for exceeding its node's
// memory was invisible, and the run would have reported "the fleet did not
// reach the end state in time, with every component still healthy". That is the
// wrong sentence about the right event.
func TestARestartedControlPlaneProcessSaysSo(t *testing.T) {
	usage := map[string]PodUsage{
		"kube-system/kube-apiserver-cp-0": {ContainerUsage: usageOf(21_288_392_704, 14204.5), Role: "kube-apiserver", Node: "cp-0"},
	}
	facts := map[string]deployedscale.PodFacts{
		"kube-system/kube-apiserver-cp-0": {
			Name: "kube-apiserver-cp-0", Node: "cp-0",
			RestartCount: 1, OOMKilled: true, LastReason: "OOMKilled",
		},
	}
	samples := ControlPlaneUsage(usage, facts)
	if len(samples) != 1 {
		t.Fatalf("%d samples", len(samples))
	}
	if !samples[0].Pod.OOMKilled || samples[0].Pod.RestartCount != 1 {
		t.Fatalf("the pod's real facts were not carried: %+v", samples[0].Pod)
	}

	// And the classifier, which is what turns it into a sentence.
	why := Classify(samples, false)
	if !strings.Contains(why, "OOM") || !strings.Contains(why, "kube-apiserver-cp-0") {
		t.Errorf("an OOM killed API server was classified as %q", why)
	}
}

// TestAPodWithNoFactsIsStillMeasured: the usage scrape and the pod list are two
// reads, and a pod that appears in one and not the other is still a process
// costing the node something.
func TestAPodWithNoFactsIsStillMeasured(t *testing.T) {
	usage := map[string]PodUsage{"kube-system/cilium-9xk2v": {ContainerUsage: usageOf(402_653_184, 88.25), Role: "cilium-agent", Node: "cp-0"}}
	samples := ControlPlaneUsage(usage, map[string]deployedscale.PodFacts{})
	if len(samples) != 1 || samples[0].Process.ResidentBytes != 402_653_184 {
		t.Fatalf("samples = %+v", samples)
	}
	if samples[0].Pod.Name != "cilium-9xk2v" {
		t.Errorf("the sample does not name its pod: %+v", samples[0].Pod)
	}
}

func componentNames(samples []deployedscale.ComponentSample) []string {
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.Component)
	}
	return out
}

// TestTheControlPlaneLineLeadsWithTheTotalAndNamesTheLargest.
//
// A node budget is spent against the total; a node is sized against the biggest
// thing on it. Both, and the count, or a reader has to add up a table to learn
// what the control plane cost.
func TestTheControlPlaneLineLeadsWithTheTotalAndNamesTheLargest(t *testing.T) {
	usage := map[string]PodUsage{
		"kube-system/kube-apiserver-cp-0":          {ContainerUsage: usageOf(21_474_836_480, 14204.5), Role: "kube-apiserver", Node: "cp-0"},
		"kube-system/etcd-cp-0":                    {ContainerUsage: usageOf(2_147_483_648, 3810.25), Role: "etcd", Node: "cp-0"},
		"kube-system/kube-controller-manager-cp-0": {ContainerUsage: usageOf(5_368_709_120, 6120.75), Role: "kube-controller-manager", Node: "cp-0"},
	}
	samples := ControlPlaneUsage(usage, nil)

	got := ControlPlaneReadout{Nodes: []string{"cp-0"}, Usage: usage, Samples: samples}.Describe()
	if !strings.Contains(got, "3 processes") {
		t.Errorf("the line does not say how many processes this is: %q", got)
	}
	// 20 + 2 + 5 GiB.
	if !strings.Contains(got, "27.0 GiB") {
		t.Errorf("the line does not carry the total resident: %q", got)
	}
	if !strings.Contains(got, "kube-apiserver-cp-0") {
		t.Errorf("the line does not name the largest process: %q", got)
	}
}

// TestAnUnreachableControlPlaneIsNotAFreeOne.
func TestAnUnreachableControlPlaneIsNotAFreeOne(t *testing.T) {
	if got := (ControlPlaneReadout{}).Describe(); !strings.Contains(got, "not") {
		t.Errorf("an unread control plane reads as %q", got)
	}
}

// TestTheLineSaysHowManyNodesAndHowManyOfEachRole.
//
// A control plane is three machines, and the figure that matters is what all
// three cost together. A line that gives a total without saying what it covers
// invites exactly the wrong reading — that it is one node's, and that the real
// number is three times larger. So the count of nodes is on the line, and so is
// the count of each role: "kube-apiserver x3" is a reader checking the total
// covers what they think it covers, without opening the table.
func TestTheLineSaysHowManyNodesAndHowManyOfEachRole(t *testing.T) {
	readout := ControlPlaneReadout{
		Nodes: []string{"cp-0", "cp-1", "cp-2"},
		Usage: map[string]PodUsage{
			"kube-system/kube-apiserver-cp-0":          {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-0"},
			"kube-system/kube-apiserver-cp-1":          {ContainerUsage: usageOf(20_401_094_656, 100), Role: "kube-apiserver", Node: "cp-1"},
			"kube-system/kube-apiserver-cp-2":          {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-2"},
			"kube-system/etcd-cp-0":                    {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-0"},
			"kube-system/etcd-cp-1":                    {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-1"},
			"kube-system/etcd-cp-2":                    {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-2"},
			"kube-system/kube-controller-manager-cp-0": {ContainerUsage: usageOf(5_368_709_120, 60), Role: "kube-controller-manager", Node: "cp-0"},
		},
	}
	got := readout.Describe()

	if !strings.Contains(got, "3 nodes") {
		t.Errorf("the line does not say how many machines this covers: %q", got)
	}
	if !strings.Contains(got, "kube-apiserver x3") {
		t.Errorf("the line does not say there are three API servers in the total: %q", got)
	}
	if !strings.Contains(got, "etcd x3") {
		t.Errorf("the line does not say there are three etcd members in the total: %q", got)
	}
	// 20 + 19 + 20 + 2 + 2 + 2 + 5 GiB = 70 GiB.
	if !strings.Contains(got, "70.0 GiB") {
		t.Errorf("the line does not carry the total across all three nodes: %q", got)
	}
	// And the per-role subtotal, so the API servers can be read as a set.
	if !strings.Contains(got, "59.0 GiB") {
		t.Errorf("the line does not subtotal the API servers: %q", got)
	}
}

// TestANodeThatCouldNotBeScrapedMakesTheTotalShortAndSaysSo.
//
// Two nodes of three summed and presented as a control plane is understating it
// by a machine — which is the same misreading the node count exists to prevent,
// arriving by a different route.
func TestANodeThatCouldNotBeScrapedMakesTheTotalShortAndSaysSo(t *testing.T) {
	readout := ControlPlaneReadout{
		Nodes:  []string{"cp-0", "cp-1", "cp-2"},
		Missed: []string{"cp-2"},
		Usage: map[string]PodUsage{
			"kube-system/kube-apiserver-cp-0": {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-0"},
			"kube-system/kube-apiserver-cp-1": {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-1"},
		},
	}
	got := readout.Describe()
	if !strings.Contains(got, "2 of 3") {
		t.Errorf("the line does not say a node is missing from the total: %q", got)
	}
	if !strings.Contains(got, "cp-2") {
		t.Errorf("the line does not name the node it could not read: %q", got)
	}
}

// TestAControlPlaneMissingAReplicaOfARoleIsFlagged.
//
// Three nodes carrying two API servers is either a scrape that missed one or a
// control plane running degraded, and both are worth a reader's attention on a
// run whose whole purpose is to find where something breaks.
func TestAControlPlaneMissingAReplicaOfARoleIsFlagged(t *testing.T) {
	readout := ControlPlaneReadout{
		Nodes: []string{"cp-0", "cp-1", "cp-2"},
		Usage: map[string]PodUsage{
			"kube-system/kube-apiserver-cp-0": {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-0"},
			"kube-system/kube-apiserver-cp-1": {ContainerUsage: usageOf(21_474_836_480, 100), Role: "kube-apiserver", Node: "cp-1"},
			"kube-system/etcd-cp-0":           {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-0"},
			"kube-system/etcd-cp-1":           {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-1"},
			"kube-system/etcd-cp-2":           {ContainerUsage: usageOf(2_147_483_648, 50), Role: "etcd", Node: "cp-2"},
		},
	}
	got := readout.Describe()
	if !strings.Contains(got, "kube-apiserver") || !strings.Contains(got, "2 of the 3 nodes") {
		t.Errorf("a role present on only two of three nodes was not flagged: %q", got)
	}
}

// TestAHealthCheckCostsAListRatherThanAScrape.
//
// The rung's health check runs every poll — every fifteen seconds for the
// length of a convergence — so it cannot be the full sample: that is a cAdvisor
// scrape per node plus five heap reads two seconds apart. What Classify reads
// is only the pod's facts, so the check only has to fetch those.
func TestAHealthCheckCostsAListRatherThanAScrape(t *testing.T) {
	facts := map[string]deployedscale.PodFacts{
		"kube-system/kube-apiserver-cp-0": {
			Name: "kube-apiserver-cp-0", Node: "cp-0",
			RestartCount: 1, OOMKilled: true, LastReason: "OOMKilled",
		},
		"kube-system/etcd-cp-0": {Name: "etcd-cp-0", Node: "cp-0"},
	}
	samples := HealthOf(facts)

	if len(samples) != 2 {
		t.Fatalf("%d samples, want one per pod", len(samples))
	}
	// No process figures: this check does not scrape, and a zero here would be
	// read as a process costing nothing if it ever reached a report.
	for _, s := range samples {
		if s.Process.ResidentBytes != 0 || s.Process.Goroutines != 0 {
			t.Errorf("%s carries process figures it never read: %+v", s.Component, s.Process)
		}
	}

	why := Classify(samples, false)
	if !strings.Contains(why, "OOM") || !strings.Contains(why, "kube-apiserver-cp-0") {
		t.Errorf("an OOM killed API server was classified as %q", why)
	}
}

// TestTheHealthCheckIsStableOrderedSoTheFirstFailureNamedIsTheSameOne.
func TestTheHealthCheckIsStableOrdered(t *testing.T) {
	facts := map[string]deployedscale.PodFacts{
		"kube-system/kube-scheduler-cp-2": {Name: "kube-scheduler-cp-2"},
		"kube-system/kube-apiserver-cp-0": {Name: "kube-apiserver-cp-0"},
		"kube-system/etcd-cp-1":           {Name: "etcd-cp-1"},
	}
	first := HealthOf(facts)
	for range 5 {
		again := HealthOf(facts)
		for i := range first {
			if first[i].Component != again[i].Component {
				t.Fatalf("order moved: %v then %v", componentNames(first), componentNames(again))
			}
		}
	}
}
