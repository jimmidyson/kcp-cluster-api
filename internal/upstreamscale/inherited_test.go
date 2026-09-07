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
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func startedAt(component string, t time.Time) deployedscale.ComponentSample {
	return deployedscale.ComponentSample{
		Component: component,
		Process:   deployedscale.ProcessSample{StartUnix: float64(t.Unix()), HeapAllocBytes: 1 << 30},
	}
}

// TestABaselineFromYesterdaysProcessIsCalledOut.
//
// The measurement this exists for: an API server that had served a
// 1500-cluster fleet the day before held 4.91 GiB of live heap against an API
// with no Clusters, no Machines and no events left in it. Restarted, on the
// same cluster with the same CRDs installed and nothing to serve, it held
// 216 MiB — twenty-three times less.
//
// So most of every memory figure that run recorded was the run before it, and
// the report said nothing, which became a stated conclusion: the control plane
// was "82% full at 1500 clusters". Most of that fullness was a fleet that no
// longer existed.
func TestABaselineFromYesterdaysProcessIsCalledOut(t *testing.T) {
	now := time.Now()
	old := Inherited([]deployedscale.ComponentSample{
		startedAt("kube-apiserver-cp-0", now.Add(-21*time.Hour)),
		startedAt("capi-controller-manager", now.Add(-time.Minute)),
	}, now)

	if len(old) != 1 {
		t.Fatalf("flagged %v, want only the process that predates the run", old)
	}
	if !strings.Contains(old[0], "kube-apiserver-cp-0") {
		t.Errorf("the wrong process was flagged: %v", old)
	}
	if !strings.Contains(old[0], "21h") {
		t.Errorf("the line does not say how old it is: %v", old)
	}

	note := DescribeInherited(old)
	if !strings.Contains(note, "not this run's") {
		t.Errorf("the note does not say whose baseline this is: %q", note)
	}
	if !strings.Contains(note, "restart") {
		t.Errorf("the note does not say what to do about it: %q", note)
	}
}

// TestAProcessStartedForThisRunIsNotFlagged. Preparing a run patches the
// managers and waits for them to settle, so a process that came up a couple of
// minutes before the first sample is this run's by any reading.
func TestAProcessStartedForThisRunIsNotFlagged(t *testing.T) {
	now := time.Now()
	old := Inherited([]deployedscale.ComponentSample{
		startedAt("capi-controller-manager", now.Add(-2*time.Minute)),
		startedAt("capd-controller-manager", now.Add(-9*time.Minute)),
	}, now)
	if len(old) != 0 {
		t.Errorf("flagged %v, but both started inside the grace this run needs to prepare", old)
	}
	if DescribeInherited(nil) != "" {
		t.Error("a clean baseline was given a caveat")
	}
}

// TestAProcessThatWillNotSayIsNotAccused.
//
// A sample with no start time reads as "cannot tell", not as "started at the
// epoch" — which would flag every process in every run as inherited and train
// a reader to skip the warning that matters.
func TestAProcessThatWillNotSayIsNotAccused(t *testing.T) {
	silent := deployedscale.ComponentSample{
		Component: "etcd-cp-0",
		Process:   deployedscale.ProcessSample{HeapAllocBytes: 1 << 30},
	}
	if old := Inherited([]deployedscale.ComponentSample{silent}, time.Now()); len(old) != 0 {
		t.Errorf("a process that publishes no start time was flagged: %v", old)
	}
	if age := silent.Process.Age(time.Now()); age != 0 {
		t.Errorf("age = %s, want zero for a process that did not say", age)
	}
}
