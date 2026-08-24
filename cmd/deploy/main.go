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

// Command deploy runs this project on a Kubernetes cluster: a kcp shard and
// one deployment per Cluster API provider, as pods.
//
// `task demo` answers "show me" in one process on one machine. This answers
// the question after it - whether the same wiring works when the managers are
// separate pods that reach kcp over the network, holding only their own
// credentials - and it is the shape an installation has.
//
// It creates the credentials, applies the objects, and then runs the demo as a
// Job, printing what the Job prints. See
// docs/site/content/en/docs/user/kubernetes.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/kubedeploy"
)

type options struct {
	namespace      string
	imageRepo      string
	imageTag       string
	kcpImage       string
	pullPolicy     string
	parent         string
	storageSize    string
	storageClass   string
	startupTimeout time.Duration

	runDemo              bool
	workspaces           int
	users                string
	clusters             int
	controlPlaneMachines int
	workerMachines       int
	demoTimeout          time.Duration
	demoArgs             string

	rolloutTimeout time.Duration
	kubeconfigOut  string
	output         string
	del            bool
}

func main() {
	opts := options{}
	// --kubeconfig is controller-runtime's, registered on flag.CommandLine
	// from an init function: this command is a client of the Kubernetes
	// cluster it deploys into, and takes it the way every other binary here
	// takes one.
	flag.StringVar(&opts.namespace, "namespace", kubedeploy.DefaultNamespace, "Namespace to install into. Created if it does not exist.")
	flag.StringVar(&opts.imageRepo, "image-repo", kubedeploy.DefaultImageRepo, "Where this repository's images are. One per binary, named after it, which is what `task image` builds with ko. The cluster has to be able to pull from it: ko.local works where the cluster is the local Docker daemon, kind.local where it is a kind cluster, and anywhere else needs a registry.")
	flag.StringVar(&opts.imageTag, "image-tag", kubedeploy.DefaultImageTag, "Their tag.")
	flag.StringVar(&opts.kcpImage, "kcp-image", kubedeploy.DefaultKcpImage, "Image to run the kcp shard from. Upstream's own by default: this project does not build a kcp.")
	flag.StringVar(&opts.pullPolicy, "image-pull-policy", string(corev1.PullIfNotPresent), "Image pull policy. The default is what a locally built image loaded into a kind cluster needs.")
	flag.StringVar(&opts.parent, "parent", demo.DefaultParent, "Workspace the APIExports are published in and the demo workspaces are created under.")
	flag.StringVar(&opts.storageSize, "storage-size", "2Gi", "Size of the shard's volume. It holds etcd, so this is the whole control plane's data.")
	flag.StringVar(&opts.storageClass, "storage-class", "", "StorageClass for the shard's volume. Empty uses the cluster's default.")
	flag.DurationVar(&opts.startupTimeout, "startup-timeout", kubedeploy.DefaultStartupTimeout, "How long a manager waits for the APIExport endpoint it discovers workspaces through. Generous, because a Deployment starts before the run that publishes it.")

	flag.BoolVar(&opts.runDemo, "demo", true, "Run the demo as a Job once everything is up. False deploys the shard and the managers and creates nothing in them.")
	flag.IntVar(&opts.workspaces, "workspaces", demo.DefaultWorkspaces, "How many workspaces the demo creates.")
	flag.StringVar(&opts.users, "users", strings.Join(demo.DefaultUsers, ","), "Tenants to share the workspaces out between. Empty means none.")
	flag.IntVar(&opts.clusters, "clusters", demo.DefaultClusters, "Clusters per workspace.")
	flag.IntVar(&opts.controlPlaneMachines, "control-plane-machines", demo.DefaultControlPlaneMachines, "Control plane machines per cluster.")
	flag.IntVar(&opts.workerMachines, "worker-machines", demo.DefaultWorkerMachines, "Worker machines per cluster.")
	flag.StringVar(&opts.demoArgs, "demo-args", "", "Extra space-separated flags for the demo run, for anything this command has no flag of its own for - e.g. \"--nutanix-export\". `go run ./cmd/demo --help` lists them.")
	flag.DurationVar(&opts.demoTimeout, "demo-timeout", 15*time.Minute, "How long the demo waits for every cluster to be ready. Longer than the local demo's, because a pod schedules, pulls and starts before it begins.")

	flag.DurationVar(&opts.rolloutTimeout, "rollout-timeout", 10*time.Minute, "How long to wait for the shard to become ready.")
	flag.StringVar(&opts.kubeconfigOut, "kubeconfig-out", ".demo/kubernetes/kcp.kubeconfig", "Where to write a kubeconfig for the deployed shard, addressed through a port-forward. Empty writes none.")
	flag.StringVar(&opts.output, "output", "", "Print the objects instead of applying them: \"yaml\". They include the generated credentials, so treat the output as a secret.")
	flag.BoolVar(&opts.del, "delete", false, "Delete the installation - the namespace and everything in it - and exit.")
	flag.Parse()

	log := funcr.New(func(prefix, args string) {
		if prefix != "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", prefix, args)
			return
		}
		fmt.Fprintln(os.Stderr, args)
	}, funcr.Options{})
	ctrl.SetLogger(log)

	err := run(ctrl.SetupSignalHandler(), opts, log)
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		// Ctrl-C. The installation is still there; stopping the command that
		// was watching it is not a failure.
	default:
		fmt.Fprintf(os.Stderr, "\ndeploy failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options, log logr.Logger) error {
	var err error
	scheme := runtime.NewScheme()
	if err = clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("building the scheme: %w", err)
	}

	if opts.del {
		cl, _, err := connect(scheme)
		if err != nil {
			return err
		}
		log.Info("Deleting the installation", "namespace", opts.namespace)
		if err := kubedeploy.Delete(ctx, cl, opts.namespace, opts.rolloutTimeout); err != nil {
			return err
		}
		fmt.Printf("Deleted namespace %s.\n", opts.namespace)
		return nil
	}

	// The credentials first, because everything else is built from them: the
	// shard is handed a certificate naming its Service, and every kubeconfig
	// is known before the first pod starts. See kubedeploy.Credentials.
	//
	// Printing the objects issues a set rather than reading the cluster's,
	// because --output answers "what would this look like" without needing a
	// cluster to answer it.
	var (
		cl        client.Client
		clientset kubernetes.Interface
		creds     kubedeploy.Credentials
	)
	if opts.output == "" {
		cl, clientset, err = connect(scheme)
		if err != nil {
			return err
		}
		existing, found, err := kubedeploy.ExistingCredentials(ctx, cl, opts.namespace)
		if err != nil {
			return err
		}
		if found {
			log.Info("Reusing the credentials this installation is already running with",
				"namespace", opts.namespace)
			creds = existing
		}
	}
	if creds.ServingCA == nil {
		creds, err = kubedeploy.NewCredentials(kubedeploy.KcpName,
			kubedeploy.ServerNames(opts.namespace), nil, kubedeploy.DefaultCertificateValidity)
		if err != nil {
			return err
		}
	}

	deployment := kubedeploy.Options{
		Namespace:        opts.namespace,
		ImageRepo:        opts.imageRepo,
		ImageTag:         opts.imageTag,
		KcpImage:         opts.kcpImage,
		ImagePullPolicy:  corev1.PullPolicy(opts.pullPolicy),
		Parent:           opts.parent,
		StorageSize:      opts.storageSize,
		StorageClassName: opts.storageClass,
		StartupTimeout:   opts.startupTimeout,
		Credentials:      creds,
	}
	if opts.runDemo {
		deployment.Demo = &kubedeploy.DemoJob{
			Workspaces:           opts.workspaces,
			Users:                splitUsers(opts.users),
			Clusters:             opts.clusters,
			ControlPlaneMachines: opts.controlPlaneMachines,
			WorkerMachines:       opts.workerMachines,
			Timeout:              opts.demoTimeout.String(),
			ExtraArgs:            strings.Fields(opts.demoArgs),
		}
	}

	objects, err := kubedeploy.Objects(deployment)
	if err != nil {
		return err
	}

	if opts.output != "" {
		return print(opts.output, objects)
	}

	// Applied in two goes rather than one, so that the shard is serving before
	// the managers are asked to find it. They tolerate the other order - that
	// is what --startup-timeout is for - but a manager that spends its first
	// minutes waiting reports nothing about whether the deployment worked.
	var (
		shard    []client.Object
		managers []client.Object
		job      []client.Object
	)
	for _, obj := range objects {
		switch {
		case obj.GetName() == kubedeploy.DemoJobName:
			job = append(job, obj)
		case isManager(obj):
			managers = append(managers, obj)
		default:
			shard = append(shard, obj)
		}
	}

	log.Info("Applying the shard", "namespace", opts.namespace, "objects", len(shard))
	if err := kubedeploy.Apply(ctx, cl, shard, log); err != nil {
		return err
	}
	log.Info("Waiting for kcp to serve", "timeout", opts.rolloutTimeout)
	if err := kubedeploy.WaitForStatefulSet(ctx, cl, opts.namespace, kubedeploy.KcpName, opts.rolloutTimeout); err != nil {
		return err
	}

	log.Info("Applying the managers", "objects", len(managers))
	if err := kubedeploy.Apply(ctx, cl, managers, log); err != nil {
		return err
	}

	written, err := writeLocalKubeconfig(opts, creds)
	if err != nil {
		return err
	}

	if len(job) == 0 {
		printNextSteps(opts, written, false)
		return nil
	}

	// A Job's pod template is immutable, so a second run replaces the first
	// rather than applying over it.
	if err := kubedeploy.ReplaceJob(ctx, cl, opts.namespace, kubedeploy.DemoJobName, opts.rolloutTimeout); err != nil {
		return err
	}
	log.Info("Running the demo", "job", kubedeploy.DemoJobName)
	if err := kubedeploy.Apply(ctx, cl, job, log); err != nil {
		return err
	}

	fmt.Println()
	// The demo's whole result is what it prints, so the Job's output is this
	// command's output.
	if err := kubedeploy.StreamJobLogs(ctx, cl, clientset, opts.namespace,
		kubedeploy.DemoJobName, os.Stdout, opts.rolloutTimeout); err != nil {
		return err
	}

	result, err := kubedeploy.WaitForJob(ctx, cl, opts.namespace, kubedeploy.DemoJobName, opts.demoTimeout)
	if err != nil {
		return err
	}
	printNextSteps(opts, written, true)
	if !result.Succeeded {
		return fmt.Errorf("the demo run failed: %s", result.Reason)
	}

	// The managers, after the fact. A demo that passed while one of them was
	// restarting is not the claim being made here - the claim is that these
	// controllers do this work from separate pods - so the pods are checked
	// rather than assumed. Short, because by now they have had the whole run
	// to become available.
	for _, manager := range managers {
		if err := kubedeploy.WaitForDeployment(ctx, cl, opts.namespace, manager.GetName(), time.Minute); err != nil {
			return fmt.Errorf("the demo passed, but %s is not running: %w", manager.GetName(), err)
		}
	}
	log.Info("Every manager is running", "managers", len(managers))
	return nil
}

// isManager is anything that reconciles: the provider deployments and the
// workspace manager. They are the layer that waits for the shard.
func isManager(obj client.Object) bool {
	if obj.GetObjectKind().GroupVersionKind().Kind != "Deployment" {
		return false
	}
	return obj.GetName() != kubedeploy.KcpName
}

func connect(scheme *runtime.Scheme) (client.Client, kubernetes.Interface, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("finding the Kubernetes cluster to deploy into (--kubeconfig, KUBECONFIG, or in-cluster): %w", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("building a client for the cluster: %w", err)
	}
	// A second client, because reading a pod's logs is a subresource
	// controller-runtime's client does not serve.
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building a clientset for the cluster: %w", err)
	}
	return cl, clientset, nil
}

// print writes the objects as a manifest, for an installation that applies its
// own YAML rather than letting this command apply it.
//
// The status subresource is dropped on the way out. Typed Go objects carry one
// whether or not it means anything, and a manifest that says a StatefulSet has
// zero ready replicas is describing the object it was built from rather than
// the one being asked for.
func print(format string, objects []client.Object) error {
	if format != "yaml" {
		return fmt.Errorf("unknown --output %q: the only format is yaml", format)
	}
	for _, obj := range objects {
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return fmt.Errorf("serialising %s: %w", obj.GetName(), err)
		}
		delete(content, "status")
		out, err := yaml.Marshal(content)
		if err != nil {
			return fmt.Errorf("serialising %s: %w", obj.GetName(), err)
		}
		fmt.Printf("---\n%s", out)
	}
	return nil
}

// writeLocalKubeconfig writes a kubeconfig for the deployed shard addressed at
// localhost.
//
// The same certificate names the Service and localhost, so one file cannot
// serve both: a client in the cluster resolves the Service and a client on
// somebody's machine cannot. This is the second one, and it works through the
// port-forward printed alongside it.
func writeLocalKubeconfig(opts options, creds kubedeploy.Credentials) (string, error) {
	if opts.kubeconfigOut == "" {
		return "", nil
	}
	raw, err := kubedeploy.Kubeconfig(
		fmt.Sprintf("https://localhost:%d", kubedeploy.KcpPort), opts.parent, opts.parent, creds)
	if err != nil {
		return "", err
	}
	if dir := filepath.Dir(opts.kubeconfigOut); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(opts.kubeconfigOut, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", opts.kubeconfigOut, err)
	}
	return opts.kubeconfigOut, nil
}

func printNextSteps(opts options, kubeconfig string, ranDemo bool) {
	fmt.Println()
	fmt.Println("Everything above ran in pods. What is in the cluster now:")
	fmt.Printf("  kubectl -n %s get pods\n", opts.namespace)
	if ranDemo {
		fmt.Printf("  kubectl -n %s logs job/%s\n", opts.namespace, kubedeploy.DemoJobName)
	}

	if kubeconfig != "" {
		fmt.Println()
		fmt.Println("Talk to the shard from here. The certificate names localhost as well as the")
		fmt.Println("Service, so the same credentials work through a port-forward:")
		fmt.Printf("  kubectl -n %s port-forward svc/%s %d:%d &\n",
			opts.namespace, kubedeploy.KcpName, kubedeploy.KcpPort, kubedeploy.KcpPort)
		fmt.Printf("  kubectl --kubeconfig %s get workspaces\n", kubeconfig)
		// One workspace, spelled out, because a kcp workspace is a URL path
		// and the first thing anybody wants after "it deployed" is to look
		// inside one. The first tenant's, since a run with tenants puts every
		// workspace inside somebody's home.
		if users := splitUsers(opts.users); ranDemo && len(users) > 0 {
			fmt.Printf("  kubectl --kubeconfig %s --context %s --server https://localhost:%d/clusters/%s:%s:%s:%s-1 get clusters,machines -A\n",
				kubeconfig, demo.BaseContext, kubedeploy.KcpPort, opts.parent,
				demo.DefaultWorkspacePrefix, users[0], demo.DefaultWorkspacePrefix)
		}
	}

	fmt.Println()
	fmt.Println("Take it all down again:")
	fmt.Printf("  go run ./cmd/deploy --delete --namespace %s\n", opts.namespace)
}

// splitUsers turns a comma-separated list into the demo's users, treating an
// empty or whitespace-only value as none rather than as one user with no name.
func splitUsers(value string) []string {
	var users []string
	for _, user := range strings.Split(value, ",") {
		if user = strings.TrimSpace(user); user != "" {
			users = append(users, user)
		}
	}
	return users
}
