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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func names(objects []client.Object) []string {
	out := make([]string, 0, len(objects))
	for _, o := range objects {
		out = append(out, o.GetName())
	}
	return out
}

// TestManagersAreNotCreatedWithKcp is the defect this file exists for.
//
// A manager resolves its APIExport's virtual workspace at startup and exits
// when it cannot, and the endpoint slice carries no endpoints until the export
// is published and a workspace has bound it. Created alongside kcp, every
// manager therefore starts into a world where neither has happened, exits, and
// crash loops — and recovers only after a backoff that grows into minutes,
// which is long enough for the run to give up on it.
func TestManagersAreNotCreatedWithKcp(t *testing.T) {
	o := testOptions()
	creds, err := NewCredentials(ServiceNames(KcpName, o.Namespace), LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	infrastructure, err := o.InfrastructureObjects(creds)
	if err != nil {
		t.Fatalf("infrastructure: %v", err)
	}
	managers, err := o.ManagerObjects()
	if err != nil {
		t.Fatalf("managers: %v", err)
	}

	for _, obj := range infrastructure {
		for _, c := range Components() {
			if obj.GetName() == c.Name {
				t.Errorf("%s is in the infrastructure phase; it would start before its APIExport exists "+
					"and crash loop", c.Name)
			}
		}
	}

	// And the infrastructure phase carries everything a manager needs to
	// exist before it: the namespace it lives in, the kubeconfig it mounts,
	// and kcp itself.
	got := names(infrastructure)
	for _, want := range []string{o.Namespace, KubeconfigSecretName, CredentialsSecretName, KcpName} {
		if !contains(got, want) {
			t.Errorf("%s is not created before the managers, so they would have nothing to mount or reach", want)
		}
	}

	if len(managers) != len(Components()) {
		t.Errorf("the manager phase has %d objects, want one per component", len(managers))
	}
	for _, obj := range managers {
		if _, ok := obj.(*appsv1.Deployment); !ok {
			t.Errorf("%T is in the manager phase", obj)
		}
	}
}

// The two phases together are still the whole run, so nothing is lost by
// splitting them.
func TestThePhasesTogetherAreTheWholeRun(t *testing.T) {
	o := testOptions()
	creds, err := NewCredentials(ServiceNames(KcpName, o.Namespace), LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	all, err := o.Objects(creds)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	infrastructure, _ := o.InfrastructureObjects(creds)
	managers, _ := o.ManagerObjects()

	if len(all) != len(infrastructure)+len(managers) {
		t.Errorf("Objects has %d, phases have %d + %d", len(all), len(infrastructure), len(managers))
	}
}

// TestContainersReportWhyTheyDied. Nothing here writes to the default
// termination log, so without this a crash reports a reason and no message.
func TestContainersReportWhyTheyDied(t *testing.T) {
	for name, spec := range podSpecs(testObjects(t, testOptions())) {
		for _, c := range spec.Containers {
			if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
				t.Errorf("%s/%s has termination policy %q; a crash would carry no message",
					name, c.Name, c.TerminationMessagePolicy)
			}
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
