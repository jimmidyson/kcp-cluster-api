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

// Package scaleharness fits resource models to sweeps that have already been
// measured, so that capacity is a number somebody derived from evidence rather
// than a number somebody remembered.
//
// # What it produces
//
// Given a committed run — a load profile, a mode, and what each swept point
// cost — a fitted model whose coefficients let a fleet shape that was never
// measured still be sized, and a hold-out validation that predicts each
// measured point from a fit that excluded it.
//
// # It no longer measures
//
// It used to: it carried a sweep driver, a service seam, load profiles and a
// delivery probe, and internal/sweep carried a second one. Two instruments
// against one process can disagree about it, and a disagreement between two
// measurements is worse than one measurement being wrong, because neither side
// is obviously at fault. internal/sweep is the instrument; departure detection,
// which only this package had, moved there with it.
//
// What is left here is arithmetic over recorded runs, and that separation is
// deliberate rather than residual: the measurement needs a real kcp and takes
// minutes, while the fit is arithmetic over its output. Keeping them apart
// means a model can be re-derived, corrected and argued with without re-running
// anything, and that the evidence a figure came from stays in the repository
// next to the figure. [SweepRun] is therefore a decoding format for committed
// evidence as much as it is a type.
//
// # Three rules this package exists to keep
//
// Reporting reuses internal/verify's outcome contract rather than inventing a
// second one. "Could not run" is a first-class result: a workspace count the
// environment cannot host is not a pass, and must never be reported as one.
//
// The departure point is found by a defined procedure, not by inspection — swept
// points, a stated tolerance, and a rule for which point counts as the departure
// point — so that two runs of one profile yield the same figure. A capacity
// derived by eye is not reproducible and therefore not a capacity.
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
// controllers, and it is not a continuous-integration gate. Its output is
// evidence for decisions, and making the project's done-condition depend on a
// multi-workspace kcp environment would hold unrelated changes hostage to it.
package scaleharness
