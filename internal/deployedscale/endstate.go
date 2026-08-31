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
	"fmt"
	"strings"
)

// The two states a checkpoint can wait for.
const (
	// EndStateEngaged is every workspace bound and holding its objects. It is
	// what a *single* deployment can be measured at, and it is what the
	// in-process deployment sweeps measure — which is what makes the
	// reconciliation a comparison of like with like rather than of one
	// deployment against four.
	EndStateEngaged = "engaged"
	// EndStateReady is every control plane ready and every Machine Ready.
	EndStateReady = "ready"
)

// resolveEndState picks what a checkpoint waits for.
//
// # Why this is not simply "ready"
//
// A cluster is taken to readiness by all four providers together: core stamps
// the topology, the bootstrap provider renders the data secret, the control
// plane provider creates the machines and the infrastructure provider brings
// them up. A run deploying only some of them can never reach that state, and
// waiting for it would not fail with "the bootstrap provider is not deployed"
// — it would time out at the first checkpoint with a machine count that never
// moved.
//
// That is exactly the specification's M1, which deploys core-manager alone to
// calibrate against the in-process core sweep. So the default is the strongest
// state the deployed set can actually reach, and asking for more than that is
// refused up front rather than discovered twenty minutes in.
func ResolveEndState(requested string, components []Component) (string, error) {
	complete := len(components) == len(Components())

	switch requested {
	case "":
		if complete {
			return EndStateReady, nil
		}
		return EndStateEngaged, nil
	case EndStateEngaged:
		return EndStateEngaged, nil
	case EndStateReady:
		if !complete {
			return "", fmt.Errorf("end state %q needs all %d providers deployed, and this run deploys %d (%s): "+
				"a cluster is taken to readiness by all four, so this run would time out at its first checkpoint "+
				"rather than fail with a reason",
				EndStateReady, len(Components()), len(components),
				strings.Join(componentNames(components), ", "))
		}
		return EndStateReady, nil
	default:
		return "", fmt.Errorf("unknown end state %q: want %q or %q", requested, EndStateEngaged, EndStateReady)
	}
}

func EndStateDescription(state string) string {
	if state == EndStateReady {
		return "every control plane ready and every Machine Ready"
	}
	return "every workspace bound and holding its objects — the state a partial provider set can reach, " +
		"and what the in-process deployment sweeps measure"
}

func componentNames(components []Component) []string {
	out := make([]string, 0, len(components))
	for _, c := range components {
		out = append(out, c.Name)
	}
	return out
}
