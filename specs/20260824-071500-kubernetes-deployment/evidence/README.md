# Evidence: the deployed topology as separate processes

## What was run, and why this way

The environment this feature was built in could not pull a container image, so
`task demo:kubernetes` could not be run in it. What could be run is everything
that is not Kubernetes: the credentials the deployment generates, kcp started
with them, every manager as its own process with the kubeconfig and the flags
its `Deployment` gives it, and the demo with `--no-manager`, which is exactly
what the `Job` runs.

`split-process-run.sh` is that run. It differs from the deployment in three
ways, all of them Kubernetes' side of the line: pods instead of processes,
mounted `Secret`s instead of files, and probes. Everything else — the PKI, both
kubeconfig flavours, the argument lists, the startup ordering — is the same.

Ports differ too: each manager is given its own `--health-addr` and
`--metrics-addr`, because a pod has a network namespace to itself and a
machine does not. That difference is how the missing `--metrics-addr` flag was
found.

## Result

`split-process-run.txt` is the end of the run. Two workspaces, a ready cluster
in each, control planes ready 1/1, every machine ready and bootstrapped, and
the isolation table with the shape a passing run has — each tenant reading
their own workspaces and refused every other tenant's. The demo exited 0.

## The image's manifest layer, checked without the image

The other thing a container changes is where the CRD manifests come from:
publishing an `APIExport` reads them out of the pinned modules, and a container
has no Go toolchain to resolve them with. The image copies them in at build
time and points `KCP_CLUSTER_API_MANIFEST_ROOT` at them.

That step is a shell loop over the module cache, so it was run outside Docker
exactly as the `Dockerfile` runs it — 75 files, 3.1 MB — and the packages that
read them (`internal/capiexports`, `internal/kcpfixtures`,
`internal/contractmetadata`) pass with the environment variable pointed at the
result. What that does not cover is the `COPY` into the final stage.

## What it found

Two faults, neither of which the single-process demo can have:

1. **Every provider manager took controller-runtime's default metrics port**
   (`:8080`) with no way to change it. Three of the four died at startup with
   `address already in use`. In pods it happens not to collide, so it would
   have stayed hidden. `--metrics-addr` is now a flag on all four.

2. **The managers could not read a tenant's `Secret`s.** Each passed its
   `--kubeconfig` config — which addresses the workspace the exports live in —
   to the ClusterCache as the *shard's* config, and the ClusterCache scopes
   what it is given to each tenant workspace itself. The result was
   `/clusters/root/clusters/<workspace>`, a 404 reported as

   ```
   error creating REST config: error getting kubeconfig secret:
   the server could not find the requested resource (get secrets demo-00-kubeconfig)
   ```

   Every cluster stopped at "control plane not yet initialized". Confirmed
   directly against the running shard: the single-scoped path answers 200 and
   the double-scoped one 404. The shard's config is now derived from the
   manager's (`providerwiring.ShardConfig`).

The second one is the reason to keep this run. It was a fault in the path
every provider takes to reach a workload cluster, in all four binaries, and it
survived because nothing had ever run those binaries against a real cluster —
the integration tests wire the same reconcilers in-process through the demo,
which passes both configs separately and so cannot have the bug.

## Reproducing it

```sh
task tools                       # the pinned kcp binary, into bin/
mkdir -p run/bin && cd run
go build -o bin/ ../cmd/core-manager ../cmd/kubeadm-bootstrap-manager \
  ../cmd/kubeadm-control-plane-manager ../cmd/dev-infrastructure-manager \
  ../cmd/workspace-manager ../cmd/demo
```

The credentials the script expects — `serving.crt`, `serving.key`,
`client-ca.crt`, `provider.kubeconfig`, `base.kubeconfig` — are the contents of
the `Secret`s `cmd/deploy` creates, and the quickest way to get a set is

```sh
go run ./cmd/deploy --output yaml
```

reading them out of the `kcp-serving-cert`, `kcp-client-ca` and
`kcp-kubeconfig` objects, with the server URL rewritten to
`https://localhost:6443`. Then `sh split-process-run.sh` from that directory.

This is a record of a run, not a supported entry point. `task demo:kubernetes`
is the supported one.
