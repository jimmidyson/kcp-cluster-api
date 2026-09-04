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

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// TestTheProfilerIsOffUntilItIsAskedFor.
//
// A manager in an installation should not serve pprof to whoever can reach its
// pod. It is on for a measurement and off otherwise, which is the same choice
// upstream Cluster API makes with its own --profiler-address.
func TestTheProfilerIsOffUntilItIsAskedFor(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var addr string
	RegisterProfilerFlag(fs, &addr)

	if addr != "" {
		t.Errorf("the profiler defaults to %q, want off", addr)
	}
	if err := fs.Parse([]string{"--profiler-address=:6060"}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if addr != ":6060" {
		t.Errorf("addr = %q after being asked for", addr)
	}
}

// TestEveryManagerNamesTheFlagTheSameWay.
//
// Four binaries have to serve pprof under one name, because one sampler reads
// all four and it addresses them by port and path. A rename in one of them
// would take that manager out of the comparison and report the other three as
// the fleet's whole cost.
func TestEveryManagerNamesTheFlagTheSameWay(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var addr string
	RegisterProfilerFlag(fs, &addr)

	flag := fs.Lookup(ProfilerFlagName)
	if flag == nil {
		t.Fatalf("no --%s flag", ProfilerFlagName)
	}
	// The usage has to say why it binds all interfaces: the samples come
	// through the API server's pod proxy, which reaches the pod IP, so
	// localhost would serve nothing a driver outside the cluster can read.
	if !strings.Contains(flag.Usage, "localhost") {
		t.Errorf("the flag does not say why it binds all interfaces: %q", flag.Usage)
	}
}
