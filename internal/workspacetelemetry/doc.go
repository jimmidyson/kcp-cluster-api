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

// Package workspacetelemetry attributes load to the workspace that produced it,
// without letting the number of exported series grow with the number of
// workspaces.
//
// # Why this exists
//
// Per-workspace controllers must disable controller-runtime's controller-name
// validation, because it records names in a process-global set that is never
// emptied (see internal/providerwiring's lifecycle contract, rule 3). The
// consequence is that every workspace's controllers share a name, so reconcile
// and queue metrics aggregate across tenants and nothing is attributable to the
// workspace that caused it.
//
// That is a reporting limitation at two workspaces. At a shard's full capacity
// it is an operational dead end: an operator who cannot tell which workspace is
// generating load cannot act on it, and capacity planning has no per-workspace
// input to work from.
//
// # The constraint that shapes the design
//
// The obvious fix — put a workspace label on the existing metrics — trades one
// scaling problem for another. A label whose cardinality is the workspace count
// is unbounded by construction, and would make the monitoring system fail at
// the scale this project exists to reach.
//
// So attribution here is deliberately *asymmetric*, because its two consumers
// want different things:
//
//   - Capacity and scaling decisions want totals — how much load this process
//     is carrying — and need no per-workspace breakdown at all.
//   - Diagnosis wants to know which workspace is hot, which in practice means
//     the outliers. Nobody diagnoses a fleet by reading a series per tenant.
//
// Exported series are therefore bounded: aggregates always, plus a labelled
// series for the busiest few workspaces and one aggregate remainder for
// everything else. Counters are tracked internally for every engaged workspace,
// since that cost is bounded by capacity; only the *exported* set is capped.
//
// See the feature's research notes (R7) for the alternatives considered and why
// they were rejected.
package workspacetelemetry
