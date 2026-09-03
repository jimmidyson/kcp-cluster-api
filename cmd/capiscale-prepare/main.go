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

// Command capiscale-prepare makes a clusterctl-installed Cluster API ready to
// be measured: Guaranteed resources and a memory ceiling on every controller, a
// pprof endpoint on every controller, and — for the DevCluster provider alone —
// the Docker socket taken away.
//
// # Why a command rather than a few lines of kubectl patch
//
// Three of the four changes apply to every controller and one applies to one of
// them, and each has a reason that is easy to get subtly wrong: GOMEMLIMIT has
// to sit below the limit rather than at it, the profiler has to bind an address
// the pod proxy can reach rather than localhost, and the socket has to go
// without taking the webhook certificate with it. The logic is in
// internal/upstreamscale where it is unit tested; this is the thing that runs
// it, so what runs and what is tested are the same code.
//
// Idempotent: a second run against a prepared cluster reports no change and
// restarts nothing, which matters because restarting a controller resets the
// process metrics the whole measurement is made of.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

// controller is one clusterctl-installed deployment and what it should be given.
type controller struct {
	name      string
	namespace string
	deploy    string
	cpu       string
	memory    string
	// devCluster is true for the one provider that ships expecting a Docker
	// socket it does not need for the in-memory backend.
	devCluster bool
}

func main() {
	var (
		kubeconfig = flag.String("kubeconfig", "", "Path to the kubeconfig of the cluster to prepare. "+
			"Defaults to the usual rules (KUBECONFIG, then ~/.kube/config).")
		kubecontext = flag.String("context", "", "Context to use. Named rather than taken from whatever "+
			"is current, because this patches deployments and the current context may be somewhere else.")
		profilerAddr = flag.String("profiler-address", ":6060",
			"Address each controller serves pprof on. Bind all interfaces, not localhost: the samples are "+
				"read through the API server's pod proxy, which reaches the pod IP.")
		dryRun = flag.Bool("dry-run", false, "Report what would change and change nothing.")
	)
	controllers := []*controller{
		{name: "core", namespace: "capi-system", deploy: "capi-controller-manager", cpu: "4", memory: "8Gi"},
		{name: "kubeadm-bootstrap", namespace: "capi-kubeadm-bootstrap-system",
			deploy: "capi-kubeadm-bootstrap-controller-manager", cpu: "2", memory: "4Gi"},
		{name: "kubeadm-control-plane", namespace: "capi-kubeadm-control-plane-system",
			deploy: "capi-kubeadm-control-plane-controller-manager", cpu: "4", memory: "6Gi"},
		{name: "devcluster", namespace: "capd-system", deploy: "capd-controller-manager",
			cpu: "6", memory: "24Gi", devCluster: true},
	}
	for _, c := range controllers {
		flag.StringVar(&c.cpu, c.name+"-cpu", c.cpu, "CPU request and limit for the "+c.name+" controller.")
		flag.StringVar(&c.memory, c.name+"-memory", c.memory,
			"Memory request and limit for the "+c.name+" controller. Raise it and re-run when a rung is "+
				"OOM killed; that loop is the point.")
		flag.StringVar(&c.namespace, c.name+"-namespace", c.namespace, "Namespace of the "+c.name+" controller.")
		flag.StringVar(&c.deploy, c.name+"-deployment", c.deploy, "Deployment name of the "+c.name+" controller.")
	}
	flag.Parse()

	if err := run(context.Background(), *kubeconfig, *kubecontext, *profilerAddr, *dryRun, controllers); err != nil {
		fmt.Fprintf(os.Stderr, "could not prepare the cluster: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, kubeconfig, kubecontext, profilerAddr string, dryRun bool, controllers []*controller) error {
	cl, err := newClient(kubeconfig, kubecontext)
	if err != nil {
		return err
	}

	var missing []string
	for _, c := range controllers {
		cpu, err := resource.ParseQuantity(c.cpu)
		if err != nil {
			return fmt.Errorf("%s: cpu %q: %w", c.name, c.cpu, err)
		}
		memory, err := resource.ParseQuantity(c.memory)
		if err != nil {
			return fmt.Errorf("%s: memory %q: %w", c.name, c.memory, err)
		}

		var d appsv1.Deployment
		key := client.ObjectKey{Namespace: c.namespace, Name: c.deploy}
		if err := cl.Get(ctx, key, &d); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, fmt.Sprintf("%s (%s/%s)", c.name, c.namespace, c.deploy))
				continue
			}
			return fmt.Errorf("reading %s: %w", key, err)
		}

		var did []string
		if upstreamscale.Guarantee(&d, cpu, memory) {
			did = append(did, fmt.Sprintf("Guaranteed at %s CPU / %s memory, with GOMEMLIMIT below it", &cpu, &memory))
		}
		if upstreamscale.Profiling(&d, profilerAddr) {
			did = append(did, "pprof on "+profilerAddr)
		}
		if c.devCluster && upstreamscale.RunWithoutDocker(&d) {
			did = append(did, "Docker socket, its hostPath volume and the privilege removed")
		}

		if len(did) == 0 {
			fmt.Printf("%-22s already prepared (QoS %s)\n", c.name, upstreamscale.QoSClass(&d))
			continue
		}
		if dryRun {
			fmt.Printf("%-22s would change: %s\n", c.name, strings.Join(did, "; "))
			continue
		}
		if err := cl.Update(ctx, &d); err != nil {
			return fmt.Errorf("updating %s: %w", key, err)
		}
		fmt.Printf("%-22s %s (QoS %s)\n", c.name, strings.Join(did, "; "), upstreamscale.QoSClass(&d))
	}

	if len(missing) > 0 {
		return fmt.Errorf("these controllers are not installed: %s. Run clusterctl init for the core, "+
			"kubeadm bootstrap, kubeadm control plane and docker providers first — and note that the "+
			"docker provider is what serves DevCluster, in-memory backend included",
			strings.Join(missing, ", "))
	}
	return nil
}

func newClient(kubeconfig, kubecontext string) (client.Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building a client config: %w", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, fmt.Errorf("building a client: %w", err)
	}
	return cl, nil
}
