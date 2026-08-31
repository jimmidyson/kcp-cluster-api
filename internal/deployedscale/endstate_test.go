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

package deployedscale

import (
	"strings"
	"testing"
)

// TestOneDeploymentCannotWaitForReadiness is the defect this file exists for.
//
// The specification's M1 deploys core-manager alone. A cluster is taken to
// readiness by all four providers together, so a run of one that waited for
// readiness would not fail saying a provider was missing — it would time out
// at its first checkpoint with a machine count that never moved, twenty
// minutes after somebody started it.
func TestOneDeploymentCannotWaitForReadiness(t *testing.T) {
	m1, err := ComponentsNamed(ComponentCore)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}

	got, err := ResolveEndState("", m1)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if got != EndStateEngaged {
		t.Errorf("M1 defaulted to %q; with one provider nothing converges and the run times out", got)
	}

	// And asking for it explicitly is refused up front rather than discovered
	// by waiting.
	_, err = ResolveEndState(EndStateReady, m1)
	if err == nil {
		t.Fatal("a single deployment was allowed to wait for readiness")
	}
	for _, want := range []string{"all 4 providers", "time out"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestAllFourReachReadiness(t *testing.T) {
	all := Components()

	got, err := ResolveEndState("", all)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if got != EndStateReady {
		t.Errorf("a complete provider set defaulted to %q, want %q", got, EndStateReady)
	}

	if got, err := ResolveEndState(EndStateReady, all); err != nil || got != EndStateReady {
		t.Errorf("explicit readiness with all four: %q, %v", got, err)
	}
}

// A complete set may still be measured at the weaker state, which is what
// makes the two comparable with each other.
func TestACompleteSetMayStillBeMeasuredAtEngaged(t *testing.T) {
	got, err := ResolveEndState(EndStateEngaged, Components())
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if got != EndStateEngaged {
		t.Errorf("got %q", got)
	}
}

func TestUnknownEndStateIsRejected(t *testing.T) {
	if _, err := ResolveEndState("converged", Components()); err == nil {
		t.Error("an unknown end state was accepted")
	}
}

// The description travels into the report, because a figure taken at
// "engaged" and one taken at "ready" are not the same measurement.
func TestEndStateDescriptionDistinguishesTheTwo(t *testing.T) {
	ready := EndStateDescription(EndStateReady)
	engaged := EndStateDescription(EndStateEngaged)
	if ready == engaged {
		t.Fatal("both end states describe themselves identically")
	}
	if !strings.Contains(engaged, "in-process deployment sweeps") {
		t.Errorf("the engaged description does not say what it is comparable with: %q", engaged)
	}
}
