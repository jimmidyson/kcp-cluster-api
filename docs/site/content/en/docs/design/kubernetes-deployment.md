---
title: Deploying on Kubernetes
description: What running the shard and the providers as pods needed, and what it deliberately leaves out.
weight: 26
---

`task demo` runs a kcp server and every provider's controllers in one process.
That is the right shape for showing what this project does and the wrong shape
for believing it: the managers share a process they would never share, the
shard is a child process that dies with the terminal, and a run that works says
nothing about whether the wiring survives being split up.

[`task demo:kubernetes`](../user/kubernetes.md) is the same run in the topology
an installation has — a shard as a `StatefulSet`, one `Deployment` per
provider, and the demo as a `Job` with its manager half switched off. The
objects are built in Go (`internal/kubedeploy`) rather than written as YAML,
for the same reason the `APIExport`s are: they are derived from the provider
list. Adding a provider adds its deployment, and a provider with no manager in
this image fails the build of an installation rather than producing a shard
that serves types nothing reconciles — which is the Nutanix provider's case,
and is why it is published without being deployed.

Three things had to be solved that a process on a laptop never meets. None of
them is about Cluster API; all three are about kcp being a server somebody else
has to reach.

## The shard's certificate has to name its Service

kcp generates a serving certificate when it is given none. That certificate
names `localhost` and `192.0.2.2` — a documentation address — and there is no
kcp flag that adds a name to it: `--external-hostname` does not exist, and
`--shard-base-url` changes the URLs kcp hands out without changing what its
certificate is valid for.

A client inside a Kubernetes cluster reaches kcp as
`kcp.<namespace>.svc.cluster.local`, which that certificate does not name, so
every manager would refuse it. So the deployment issues the certificate before
kcp starts and passes `--tls-cert-file`/`--tls-private-key-file`. The names it
carries are the Service in each of the four forms Kubernetes resolves it, plus
`localhost` — the last one so that the same certificate serves a
`kubectl port-forward` from somebody's machine, which is what makes one
kubeconfig usable from both sides.

## The credentials have to exist before the shard does

kcp mints two identities for itself at first start and writes them into its
state directory as bearer tokens: `kcp-admin`, in the group `system:kcp:admin`,
and `shard-admin`, in `system:masters`. Both matter here — the demo needs the
second one to impersonate tenants, because kcp scopes an impersonated user to
one logical cluster unless the impersonator is privileged.

Inside a pod that directory is unreachable. Fetching those tokens out of it
would need a sidecar that watches the file and writes a `Secret`, or a volume
shared between pods — machinery whose only job is to undo the fact that the
credentials were generated in the wrong place.

So they are not generated there. The deployment issues a client CA, hands it to
kcp with `--client-ca-file`, and mints client certificates whose common name
and organization are those two identities. kcp authenticates a client
certificate exactly as a Kubernetes API server does, so the certificates
authenticate as the same users the tokens do — verified against a running kcp
with a `SelfSubjectReview`, which is where the constants in
`internal/kubedeploy` come from. Every kubeconfig is then known before the
first pod starts, and nothing has to read anything back out of the shard.

Nothing rotates any of it. A year's validity, and a redeploy issues a new set:
these are an installation's credentials, not a credential to keep.

### Two kubeconfigs, not one

The `Secret` holds two files that differ only in their current context.

A provider manager reads its `APIExportEndpointSlice` out of the workspace the
exports live in, so its config has to address that workspace. The workspace
manager scopes itself to `--provider-workspace` and so needs a cluster-unaware
one; handed a scoped config it would build a URL with two `/clusters/` segments
in it. Both take their config through controller-runtime's `--kubeconfig`,
which has no `--context` to go with it — a manager gets its file's current
context or nothing. So the choice is made by which file each `Deployment`
mounts.

## A manager has to wait for what it cannot create

A process somebody starts by hand runs after the exports are published, because
they published them. A `Deployment` starts when Kubernetes starts it.

Two waits fall out of that, and before this both were failures:

- A provider manager resolves its export's virtual workspace before it wires
  anything, and kcp gives an export an endpoint only once a workspace has bound
  it. The first workspace is created by the demo run, which is a `Job` that
  Kubernetes schedules alongside the managers rather than before them.
- The workspace manager reads the `cluster-api` `WorkspaceType` at startup, and
  that type is published by the same run.

Both now wait, bounded by `--startup-timeout`, and the deployment sets it to
thirty minutes. Exiting instead is not harmless: the pod backs off
exponentially, so a wait of seconds becomes minutes of `CrashLoopBackOff`, and
an installation applied in one go looks broken while it is converging. Only
"not found" is waited out — a forbidden read will not resolve itself by being
asked again, and waiting out the timeout would bury the reason under a
deadline.

The probes follow from the same fact. A manager binds its health endpoint when
it starts its controllers, which is *after* that wait, so a liveness probe
alone would kill a manager for waiting exactly as long as it was told to. Each
gets a startup probe whose budget is the startup timeout, and liveness takes
over once it passes.

## What running it this way found

**The metrics endpoint had no flag.** controller-runtime binds `:8080` by
default, and nothing here overrode it, so two managers on one machine could not
both start — the second failed with `address already in use` in the middle of a
log nobody reads until something is wrong. In pods it happens not to collide,
which is exactly why it would have stayed unnoticed. `--metrics-addr` is now a
flag on every provider manager, and the port is named in the pod spec so a
scrape configuration has something to name.

**The managers could not read a tenant's Secrets.** Every provider manager
passed its `--kubeconfig` config to the ClusterCache as the shard's config.
That config addresses the workspace the exports live in, because that is where
its `APIExportEndpointSlice` is; the ClusterCache scopes what it is given to
each tenant workspace by appending `/clusters/<workspace>` to it. The two
together produce `/clusters/root/clusters/<workspace>`, which kcp answers with
a 404 — reported as

```
error creating REST config: error getting kubeconfig secret:
the server could not find the requested resource (get secrets demo-00-kubeconfig)
```

which is a message about a `Secret`, from a controller doing nothing wrong,
about a URL nobody looks at. Every cluster stopped at "control plane not yet
initialized", and each provider's log blamed the one before it.

The demo never hit it: it passes the shard's own config and the scoped one
separately, because it has both. The binaries have one kubeconfig, so the
shard's config is now derived from it — `providerwiring.ShardConfig`, which
strips the workspace from the host and is the same server as the same user.

This one had nothing to do with Kubernetes. It was a bug in the manager
binaries, in the path every provider takes to reach a workload cluster, and it
survived because nothing ran those binaries against a real cluster: the
integration tests wire the same reconcilers in-process, through the demo,
which is the arrangement that does not have the bug.

That is the argument for this whole shape in two bugs: the single-process demo
cannot fail either way, and an installation can.

## What it deliberately does not do

**One replica each, and no leader election.** The controllers hold no lease, so
a second replica would reconcile every workspace twice. The `Deployment`s say
`Recreate` for the same reason: a rolling update would run two for a moment.
Leader election across a fleet of workspaces is not built, and until it is,
scaling out is horizontal *sharding* — a kcp `Partition` per replica group,
which is [D6](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md)
in the plan, not a replica count.

**One shard.** A `StatefulSet` of one, holding etcd in its volume. Multi-shard
is the same D6 question.

**No RBAC, no ServiceAccounts.** Nothing here talks to the Kubernetes API
server it runs on: the managers talk to kcp, with credentials from a `Secret`.
The pods run with the default service account and need nothing from it.

**No webhooks.** They are single-workspace by construction until the webhook
dispatch layer lands, and the demo creates fully specified objects for that
reason — see [Per-workspace wiring](per-workspace-wiring.md).

**The demo run publishes the exports.** An installation would publish them once
and separately, and tenants would arrive afterwards; here the same `Job` that
creates the workspaces also creates the exports and the `WorkspaceType`. That
is why the managers wait rather than being ordered after a bootstrap step, and
splitting the two is what a `--demo=false` installation needs before it is
useful.

**Nothing is measured about it.** Every per-workspace figure this project
publishes was taken from the single-process shape
([Workspace resource usage](workspace-resource-usage.md)), and a deployment
that splits the managers across pods has not been swept. Whether the numbers
move is an open question, not an answered one.
