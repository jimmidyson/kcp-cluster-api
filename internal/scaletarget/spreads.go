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

package scaletarget

import (
	"fmt"
	"strings"
)

// densestDefaultSpread caps how many clusters an automatically chosen second
// spread puts in one workspace.
//
// Some cap is needed because the point of the second spread is to hold the
// fleet in fewer workspaces, and taken to its limit that is one workspace —
// which has no workspace count to vary and therefore no per-workspace slope to
// fit. Ten is a judgement: dense enough that the per-workspace and per-cluster
// terms separate visibly, loose enough to leave a fleet's worth of workspaces
// behind it.
const densestDefaultSpread = 10

// minimumDefaultWorkspaces is how many workspaces a chosen spread must leave.
//
// Two, because a slope needs two points. A spread that collapsed the fleet into
// a single workspace would produce a run whose per-workspace figure is "not
// measured", which is a worse default than not running that spread at all.
const minimumDefaultWorkspaces = 2

// DefaultSpreads picks how to spread a fleet when nobody said.
//
// One cluster per workspace always, and a denser second spread when one
// divides the fleet cleanly and still leaves workspaces to count. The pair is
// what separates what a workspace costs from what a cluster costs; a single
// spread measures their sum.
//
// It is derived rather than fixed because a fixed default belongs to a fixed
// cluster count. `1,10` is right for two hundred clusters and impossible for
// two, and tuning the cluster count should not break a knob nobody touched.
func DefaultSpreads(clusters int) []int {
	if clusters < 1 {
		return nil
	}
	for d := densestDefaultSpread; d > 1; d-- {
		if clusters%d == 0 && clusters/d >= minimumDefaultWorkspaces {
			return []int{1, d}
		}
	}
	return []int{1}
}

// Divisors are the spreads a cluster count can actually be divided into, up to
// a cap, so a refusal can say what would have worked.
func Divisors(clusters, upTo int) []int {
	var out []int
	for d := 1; d <= clusters && d <= upTo; d++ {
		if clusters%d == 0 {
			out = append(out, d)
		}
	}
	return out
}

// describeDivisors renders Divisors for an error message.
func describeDivisors(clusters int) string {
	divisors := Divisors(clusters, clusters)
	if len(divisors) > 12 {
		divisors = divisors[:12]
	}
	parts := make([]string, 0, len(divisors))
	for _, d := range divisors {
		parts = append(parts, fmt.Sprint(d))
	}
	return strings.Join(parts, ", ")
}
