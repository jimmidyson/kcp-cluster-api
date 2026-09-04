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

package managermetrics

import "github.com/spf13/pflag"

// ProfilerFlagName is what every manager calls the flag that serves pprof.
//
// # Why a shared name rather than four flags that happen to match
//
// One sampler reads all four managers and addresses them by port and path, so
// a rename in one binary takes that manager out of a measurement without
// taking it out of the fleet — the run reports three managers' cost as the
// whole of it. The name is upstream Cluster API's, which is what the stock side
// of the comparison is started with.
const ProfilerFlagName = "profiler-address"

// RegisterProfilerFlag adds --profiler-address to a manager's flags.
//
// # Why a manager needs one at all
//
// A heap read from /metrics is go_memstats_heap_alloc_bytes at whatever point
// of the collector's sawtooth the scrape landed on. A heap read from pprof with
// gc=1 is the retained set, because the profile forces a collection first. They
// are different quantities, and three runs of the same fleet disagreed by a
// factor of four before that was understood.
//
// The stock side of the comparison is sampled through pprof, so this side has
// to be as well, or the difference between the two numbers is the instrument
// rather than the system. See internal/upstreamscale.Sampler.Process.
//
// Off by default: a manager in an installation should not serve pprof to
// whatever can reach its pod, and this is a measurement's flag.
func RegisterProfilerFlag(fs *pflag.FlagSet, addr *string) {
	fs.StringVar(addr, ProfilerFlagName, "",
		"Address to serve pprof on, e.g. :6060. Empty disables it. Bind all interfaces rather than "+
			"localhost: the samples come through the API server's pod proxy, which reaches the pod IP, so "+
			"a profiler on localhost serves nothing a driver outside the cluster can read. A heap profile "+
			"taken with gc=1 is the retained set, which is what a scale run compares; the same figure read "+
			"from /metrics is a point on the collector's sawtooth.")
}
