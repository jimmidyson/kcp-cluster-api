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

	corev1 "k8s.io/api/core/v1"
)

// TestKcpCanReachTheAddressItAdvertises is the property behind a failure that
// took several rounds to place.
//
// kcp is told to advertise the Service name as its shard URL, and its own
// apibinder initializer resolves the APIExports it binds through that address.
// If kcp cannot reach it, the default APIBindings never bind, the
// system:apibindings initializer is never removed, and every workspace sits in
// Initializing for ever — reported against whatever created the workspace
// rather than against the server.
//
// Confirmed by experiment: kcp started with an advertised address it could not
// reach hung workspaces in exactly that way, and the same kcp advertising an
// address it could reach did not.
//
// A virtual IP does not satisfy it. A pod dialling a ClusterIP whose only
// endpoint is itself is the hairpin case, and it is not dependable. Headless
// resolves the name straight to the pod, so kcp reaches itself at its own
// address.
func TestKcpCanReachTheAddressItAdvertises(t *testing.T) {
	o := testOptions()
	svc := o.KcpService()

	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("the kcp Service has a virtual IP (%q). kcp must reach the address it advertises, and a pod "+
			"dialling its own ClusterIP is the hairpin case — where it fails, every workspace hangs in "+
			"Initializing and nothing says why", svc.Spec.ClusterIP)
	}

	// And the address kcp is told to advertise is that Service, so the two
	// cannot drift apart.
	args := strings.Join(KcpArgs(o.KcpBaseURL(), "/data", CredentialsMountPath, KcpPort), " ")
	if !strings.Contains(args, "--shard-base-url="+o.KcpBaseURL()) {
		t.Errorf("kcp is not told to advertise %s", o.KcpBaseURL())
	}
	if !strings.Contains(o.KcpBaseURL(), svc.Name+"."+o.Namespace+".svc") {
		t.Errorf("the advertised address %q is not this Service's name", o.KcpBaseURL())
	}
}

// The serving certificate has to cover the advertised name too, or kcp
// reaching itself fails on TLS rather than on routing — the same hang from a
// different cause.
func TestTheAdvertisedNameIsInTheCertificate(t *testing.T) {
	o := testOptions()
	names := ServiceNames(KcpName, o.Namespace)

	want := KcpName + "." + o.Namespace + ".svc"
	found := false
	for _, n := range names {
		if n == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the serving certificate does not cover %q, which is the address kcp advertises and dials "+
			"itself at: %v", want, names)
	}
}
