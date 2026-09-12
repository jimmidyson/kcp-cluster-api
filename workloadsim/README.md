# workloadsim

Fakes the workload cluster behind any Cluster API infrastructure provider, so
that a management cluster can be driven to Machines in the `Running` phase with
no real nodes. It is the provider-agnostic half of what the docker/dev
provider's in-memory backend does for `DevMachine`: an in-memory API server per
`Cluster`, and a `Node` per `Machine`.

It is its own Go module, pinned to stock Cluster API and importing nothing from
the rest of this repository, so it can be built or imported without the kcp
fork. See the [user documentation](../docs/site/content/en/docs/user/workloadsim.md)
for what it does, what it deliberately does not do, and how to run it against
CAPX and `ntnx-sim`.

```sh
cd workloadsim && go build -o ../bin/workloadsim ./cmd/workloadsim
bin/workloadsim --kubeconfig $KUBECONFIG --host 127.0.0.1 --port-min 20000 --port-max 30000
```
