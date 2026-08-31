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

// Package managermetrics gives every manager deployment the same metrics
// endpoint, serving the Go runtime and process collectors alongside
// controller-runtime's own.
//
// # Why this exists
//
// controller-runtime's metrics registry is a bare prometheus.NewRegistry()
// (pkg/metrics/registry.go). It is not the default registerer, so it carries
// none of the collectors the default one has by default: a manager built on
// controller-runtime serves workqueue, reconcile and client-go metrics, and no
// go_goroutines, no go_memstats_*, and no process_resident_memory_bytes at all.
//
// That is a gap in its own right — a controller process whose memory and
// goroutine count cannot be scraped is under-instrumented, whatever else it
// reports. It is also specifically what a deployed measurement needs: those
// three are the quantities this repository's in-process instruments report, so
// they are the only ones by which a deployed run and an in-process run of the
// same fleet can be checked against each other. Without them a deployed figure
// would be a second number with nothing to reconcile it to, which is the
// failure mode
// `specs/20260831-210000-deployed-fleet-scale/spec.md` sets out to avoid.
package managermetrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus/collectors"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// DefaultBindAddress is what a manager serves metrics on unless told
// otherwise. It matches controller-runtime's own default, so adding the flag
// changed no deployment's behaviour.
const DefaultBindAddress = ":8080"

// register is done once per process. Registering a collector twice panics, and
// a manager that dies at startup because two call sites both wanted metrics is
// a worse failure than the duplicate it is protecting against.
var register sync.Once

// Register adds the Go runtime and process collectors to controller-runtime's
// registry. Safe to call more than once.
//
// Errors are deliberately swallowed rather than returned. The only way these
// fail is an AlreadyRegisteredError, which the sync.Once already prevents and
// which would mean metrics were present rather than absent — refusing to start
// a manager over it would trade a complete process for a complete metrics
// endpoint.
func Register() {
	register.Do(func() {
		_ = metrics.Registry.Register(collectors.NewGoCollector())
		_ = metrics.Registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	})
}

// Options builds the manager's metrics server options and registers the
// collectors that make the endpoint worth scraping.
//
// The two are one call because they are one decision: an endpoint without the
// runtime collectors is the state this package exists to correct, and a caller
// that could take the endpoint without them would be able to reintroduce it.
//
// bindAddress follows controller-runtime's convention — "0" disables the
// server, empty means the default.
func Options(bindAddress string) metricsserver.Options {
	Register()
	return metricsserver.Options{BindAddress: bindAddress}
}
