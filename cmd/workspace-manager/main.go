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

// Command workspace-manager onboards KCP workspaces to Cluster API and keeps
// their permissions right as providers come and go.
//
// It is the deployment behind two sentences a tenant should be able to take at
// face value:
//
//   - "Create a workspace of type cluster-api and it can run clusters." The
//     WorkspaceType binds Cluster API's core APIExport; this process's
//     initializer writes the roles that say who may use it, before kcp lets
//     the workspace become Ready.
//
//   - "Enable the provider you want, and nothing else has to happen." A
//     provider's APIExport appearing rewrites core's permission claims so that
//     core's controllers can reach its types; kcp's own Maintain lifecycle
//     accepts those claims in every workspace; and a tenant binding that
//     provider widens their own workspace's roles to cover its API group.
//
// It reconciles no Cluster API object. Everything it writes is a permission -
// see internal/workspacemanager and docs/adr-0001-provider-api-permissions.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jimmidyson/kcp-cluster-api/internal/workspacemanager"
)

var (
	setupLog = ctrl.Log.WithName("setup")

	providerPath string
	timeout      time.Duration

	logOptions = logs.NewOptions()
)

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&providerPath, "provider-workspace", "root",
		"Path of the workspace holding the Cluster API APIExports and the cluster-api WorkspaceType. "+
			"This is where permission claims are maintained and where the WorkspaceType's initializer is registered.")
	fs.DurationVar(&timeout, "startup-timeout", time.Minute,
		"How long to wait for the things this manager cannot create for itself: the cluster-api "+
			"WorkspaceType being published, the initializing virtual workspace URL kcp puts on it, and "+
			"the onboarding APIExport's endpoint. The last appears only once a workspace has bound the "+
			"export, so a process started before its first tenant waits here - and so does a Deployment "+
			"applied alongside the run that publishes the type. Raise it for an installation whose "+
			"tenants arrive later than its controllers.")
}

func main() {
	initFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	if err := pflag.CommandLine.Set("v", "2"); err != nil {
		fmt.Printf("Failed to set default log level: %v\n", err)
		os.Exit(1)
	}
	pflag.Parse()

	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		fmt.Printf("Unable to start manager: %v\n", err)
		os.Exit(1)
	}
	ctrl.SetLogger(klog.Background())

	ctx := ctrl.SetupSignalHandler()

	runner, err := workspacemanager.New(ctx, workspacemanager.Options{
		BaseConfig:   ctrl.GetConfigOrDie(),
		ProviderPath: providerPath,
		Timeout:      timeout,
	})
	if err != nil {
		setupLog.Error(err, "Unable to set up the workspace manager")
		os.Exit(1)
	}

	setupLog.Info("Starting manager", "providerWorkspace", providerPath)
	if err := runner.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
