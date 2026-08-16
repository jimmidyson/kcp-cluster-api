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

// Package scaleharness measures what a workspace costs this process, so that
// capacity is a number somebody measured rather than a number somebody
// remembered.
//
// # What it produces
//
// A sweep across geometrically spaced workspace counts on a named load profile,
// reporting engagement latency, per-event delivery cost, per-workspace
// footprint and throughput; the point at which cost departs from linear; and a
// fitted resource model whose coefficients let a fleet shape that was never
// measured still be sized.
//
// # Three rules this package exists to keep
//
// Reporting reuses internal/verify's outcome contract rather than inventing a
// second one. "Could not run" is a first-class result: a workspace count the
// environment cannot host is not a pass, and must never be reported as one.
//
// The departure point is found by a defined procedure, not by inspection — swept points,
// a stated tolerance, and a rule for which point counts as the departure point — so that
// two runs of one profile yield the same figure. A capacity derived by eye is
// not reproducible and therefore not a capacity.
//
// Every figure records how its load was produced. Synthetic load can
// under-measure, because generated objects may fail validation or take cheap
// error paths instead of the reconcile paths a real tenant would exercise. A
// figure that does not say whether it was synthetic or observed must not be
// used for sizing, since an under-measured memory figure becomes an
// under-provisioned limit.
//
// # What is deliberately not here
//
// This is not a general-purpose characterisation utility for arbitrary
// controllers. The sweep, fit and departure point machinery is service-agnostic — the cost
// structure it measures is a property of the per-workspace wiring rather than
// of any one controller — but the service-specific parts sit behind a narrow
// interface with a single production implementation, because there is one
// controller today. Generalising is triggered by the second one.
//
// It is also not a continuous-integration gate. Its output is evidence for
// decisions, and making the project's done-condition depend on a
// multi-workspace kcp environment would hold unrelated changes hostage to it.
package scaleharness
