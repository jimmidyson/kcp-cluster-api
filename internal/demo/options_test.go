/*
Copyright 2026 The kcp-cluster-api Authors.

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

package demo

import (
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func validOptions() Options {
	return Options{
		BaseConfig:           &rest.Config{},
		Workspaces:           1,
		ClustersPerWorkspace: 1,
		ControlPlaneMachines: 1,
		Backend:              BackendInMemory,
		// Supplied for the same reason Backend is: validate() runs after
		// applyDefaults, and both fields reject an unset value rather than
		// silently choosing for the caller.
		Onboarding: OnboardingWorkspaceType,
	}
}

// TestValidateRequiresAControlPlane covers the rule that replaced the demo's
// no-control-plane mode.
//
// That mode predates ClusterClass. It existed when a demo cluster was a Cluster
// with an infrastructureRef and nothing else, so asking for no machines really
// did mean no control plane types were needed. Once every cluster became
// ClusterClass based the class always named a KubeadmControlPlaneTemplate and
// Blueprint always created one, and the mode became a way to ask for a
// blueprint whose kinds nobody had bound — which failed at cluster creation
// with "no matches for kind", a long way from the flag that caused it.
func TestValidateRequiresAControlPlane(t *testing.T) {
	t.Run("zero is rejected, naming the reason", func(t *testing.T) {
		opts := validOptions()
		opts.ControlPlaneMachines = 0

		err := opts.validate()
		if err == nil {
			t.Fatal("validate() accepted ControlPlaneMachines = 0")
		}
		if !strings.Contains(err.Error(), "at least 1") {
			t.Errorf("validate() said %q, want it to say the minimum", err)
		}
	})

	t.Run("negative is rejected too", func(t *testing.T) {
		opts := validOptions()
		opts.ControlPlaneMachines = -1

		if err := opts.validate(); err == nil {
			t.Fatal("validate() accepted ControlPlaneMachines = -1")
		}
	})

	t.Run("one is accepted", func(t *testing.T) {
		if err := validOptions().validate(); err != nil {
			t.Fatalf("validate() rejected a valid options: %v", err)
		}
	})

	// Workers used to need their own check, because a control plane could be
	// absent for them to join. It cannot be now, so the rule above subsumes it.
	t.Run("workers no longer need a separate rule", func(t *testing.T) {
		opts := validOptions()
		opts.WorkerMachines = 2

		if err := opts.validate(); err != nil {
			t.Fatalf("validate() rejected workers alongside a control plane: %v", err)
		}
	})
}

// TestProvidersAlwaysPublishAllFour is the other half of the same change: the
// blueprint names bootstrap and control plane types unconditionally, so their
// exports have to be published unconditionally too.
func TestProvidersAlwaysPublishAllFour(t *testing.T) {
	for _, machines := range []int{1, 3} {
		opts := validOptions()
		opts.ControlPlaneMachines = machines

		if got := len(opts.providers()); got != 4 {
			t.Errorf("providers() returned %d exports for %d control plane machines, want 4",
				got, machines)
		}
	}
}
