//go:build integration

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

package scale_test

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness"
)

var engageTimeout = flag.Duration("engage-timeout", 5*time.Minute,
	"How long to wait for every provisioned workspace to be engaged before a measurement point is abandoned.")

// kcpFleet provisions workspaces against a real kcp, accumulating rather than
// rebuilding.
//
// Accumulating is both cheaper and more faithful: a shard grows, and the
// question is what a process holding N workspaces costs, not what it costs to
// have created N of them from nothing.
type kcpFleet struct {
	t       *testing.T
	ctx     context.Context
	server  kcptestingserver.RunningServer
	baseCfg *rest.Config
	scheme  *runtime.Scheme

	clients []scaleharness.Workspace
}

func (f *kcpFleet) Provision(ctx context.Context, workspaces int) ([]scaleharness.Workspace, error) {
	for len(f.clients) < workspaces {
		wsPath, ws := kcptesting.NewWorkspaceFixture(f.t, f.server, logicalcluster.NewPath("root"))
		cfg := kcpclient.SetCluster(rest.CopyConfig(f.baseCfg), wsPath)
		c, err := client.New(cfg, client.Options{Scheme: f.scheme})
		if err != nil {
			return nil, fmt.Errorf("client for workspace %d: %w", len(f.clients), err)
		}
		if err := kcpfixtures.BindExport(ctx, c, kcpfixtures.BindExportOptions{
			BindingName:  bindingName,
			ExportPath:   "root",
			ExportName:   exportName,
			ReadyTimeout: 90 * time.Second,
		}); err != nil {
			return nil, fmt.Errorf("binding workspace %d: %w", len(f.clients), err)
		}
		// The logical cluster name, not the path: it is what the provider
		// engages under and what a controller sees, so it is the only
		// identity a delivery can be matched against.
		f.clients = append(f.clients, scaleharness.Workspace{
			Name:   ws.Spec.Cluster,
			Client: c,
		})
	}
	return f.clients[:workspaces], nil
}

func parsePoints(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("parsing points %q: %w", s, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("point %d is not a workspace count", n)
		}
		out = append(out, n)
	}
	return out, nil
}
