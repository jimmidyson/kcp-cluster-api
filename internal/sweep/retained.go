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

package sweep

// Retained reports what the workspaces that have left are still costing, per
// departure.
//
// It compares a teardown sample with the sample taken at the same workspace
// count on the way up, because that is the only comparison that isolates the
// answer: both describe a process serving k workspaces, so any difference is
// what the departed workspaces did not give back.
//
// The comparison is made at one workspace — the point where the most have left
// while at least one remains — rather than at zero. The last departure also
// shuts the shared wildcard cache down, because kcp empties the
// APIExportEndpointSlice when the last APIBinding goes, and that is a fixed
// cost coming off rather than a workspace's share.
//
// ok is false when the sweep cannot answer: fewer than two workspaces, or
// either endpoint of the comparison missing.
func Retained(samples []Sample, measure func(Sample) float64) (perDeparture float64, departed int, ok bool) {
	var up, down *Sample
	peak := 0
	for i := range samples {
		s := &samples[i]
		if s.Phase == PhaseActive && s.Workspaces > peak {
			peak = s.Workspaces
		}
		if s.Workspaces != 1 {
			continue
		}
		switch s.Phase {
		case PhaseActive:
			up = s
		case PhaseDisengaged:
			down = s
		case PhaseBaseline, PhaseEngaged:
		}
	}
	if up == nil || down == nil || peak < 2 {
		return 0, 0, false
	}
	departed = peak - 1
	return (measure(*down) - measure(*up)) / float64(departed), departed, true
}
