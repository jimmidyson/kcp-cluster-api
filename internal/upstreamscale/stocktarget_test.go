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
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fleetOf(t *testing.T, ready int) client.Client {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s)
	for i := range ready {
		c := cluster("c"+strings.Repeat("0", 3)+string(rune('0'+i)), true)
		m := machine("m"+string(rune('0'+i)), true)
		b = b.WithObjects(&c, &m)
	}
	return b.Build()
}

// TestConvergenceIsReadFromTheWatchOnceThereIsOne.
//
// The poll used to list every Cluster and every Machine as full objects every
// fifteen seconds, through the VIP — about 90 MB of JSON per poll at 15,000
// Machines, all of it landing on the one API server whose node's etcd member
// the VIP's own lease has to write through. That instance served two and a
// half times the lists of its peers. A watch costs one paged list and then
// events, and a poll is a walk over memory.
func TestConvergenceIsReadFromTheWatchOnceThereIsOne(t *testing.T) {
	target := &StockTarget{Client: fleetOf(t, 0)}
	target.fleet = fleetOf(t, 2)

	got, err := target.Converged(context.Background(), 2, 2)
	if err != nil {
		t.Fatalf("converged: %v", err)
	}
	if !got.Done {
		t.Errorf("the fleet the watch holds was not counted: %+v", got)
	}
}

// TestConvergenceFallsBackToTheClientBeforeAWatchExists, so a target that was
// never prepared, or one whose cache could not start, still counts.
func TestConvergenceFallsBackToTheClientBeforeAWatchExists(t *testing.T) {
	target := &StockTarget{Client: fleetOf(t, 2)}

	got, err := target.Converged(context.Background(), 2, 2)
	if err != nil {
		t.Fatalf("converged: %v", err)
	}
	if !got.Done {
		t.Errorf("the fleet was not counted from the client: %+v", got)
	}
}
