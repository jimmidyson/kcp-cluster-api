# workloadsim

Fakes the workload cluster behind any Cluster API infrastructure provider, so
that a management cluster can be driven to a `KubeadmControlPlane` at its
declared replica count and `Machines` in the `Running` phase with no real
nodes. It is the provider-agnostic counterpart of the docker/dev provider's
in-memory backend: an in-memory API server per `Cluster`, a `Node` per
`Machine`, and on a control plane node the etcd member, static Pods and
kubeadm objects a `KubeadmControlPlane` inspects.

It is its own Go module, pinned to stock Cluster API and importing nothing from
the rest of this repository, so it can be built or imported without the kcp
fork. See the [user documentation](../docs/site/content/en/docs/user/workloadsim.md)
for what it does, what it proves, and how to plug it into the CAPX scale
harness (`simrun-workloadsim.patch` is that change, for the fork branch).

```sh
cd workloadsim && go build -o ../bin/workloadsim ./cmd/workloadsim
bin/workloadsim --kubeconfig $KUBECONFIG --host 127.0.0.1 --port-min 20000 --port-max 30000
```

Tests:

```sh
cd workloadsim && go test ./...     # unit tests, no binaries needed
task test:workloadsim:e2e          # stock Cluster API managers against workloadsim, needs envtest
```
