---
title: The Nutanix infrastructure provider
description: Why it is a separate Go module, and the four things that had to change in it before one process could serve many tenants.
weight: 29
---

The Nutanix infrastructure provider — CAPX — is the first provider this project
integrates that it does not also maintain. It is the test of
[ADR-0004](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0004-scaling-to-many-provider-forks.md):
a fork carrying the patches, an integration module here, and nothing shared
between them that did not have to be.

## It is a separate Go module

`providers/nutanix-infrastructure/` is its own module rather than another
`cmd/` directory, because of what it drags in:

| | provider module | this repository |
|---|---|---|
| `aws-sdk-go-v2` | 12 | 0 |
| `ntnx-api-golang-clients` | 8 | 0 |

The Nutanix SDK is ten modules and pulls a further twelve of the AWS SDK
behind it. None of that belongs in the dependency graph of a repository whose
other three managers never speak to Nutanix, and a provider whose SDK is
absent from that graph cannot break its builds.

Two things about the boundary are worth knowing before adding a second one:

**`replace` directives do not propagate.** Go honours `replace` only in the
main module, so the provider module restates all four of this repository's
pins — Cluster API's three and Nutanix's one. Without them it resolves genuine
upstream Cluster API alongside the fork, which fails confusingly rather than
cleanly. The pins have to move together: the two modules carrying different
versions of the same fork would build two different CAPX into one repository.

**`internal/` is reachable from a nested module.** Go's internal rule is
path-prefix based rather than module based, so
`github.com/jimmidyson/kcp-cluster-api/providers/nutanix-infrastructure` can
import `internal/coremanager` while a module outside that prefix cannot. That
is what lets a provider module reuse the fleet wiring without any of it being
promoted to a public API.

Because `./...` does not reach a nested module, `task build`, `task lint` and
`task test:unit` iterate the provider modules explicitly. A module left out of
that list is one CI never compiles, which is how it stops compiling.

## What had to change in CAPX

Four things resolved a tenant-scoped resource by a name that is unique only
within one API server. Under kcp a workspace *is* an API server and one process
serves many, so each of them confused two tenants. They are carried on the
fork's `kcp/v1.11` branch.

Three share a shape — qualify the name by the logical cluster the `Cluster` was
read from, taken from kcp's `kcp.io/cluster` annotation on the object rather
than from configuration. An empty logical cluster means "do not qualify", so a
CAPX running outside kcp behaves exactly as it did.

**VM names.** A VM was named for its `Machine`, and looked up by a
Prism-wide `name eq` query that refuses to resolve once two VMs share a name.
Two workspaces with a same-named `Machine` broke each other's reconcile.

**The CAPI category.** The category marking what CAPX owns was keyed on the
cluster's name alone, so two workspaces holding a `demo` addressed one
category — and teardown deletes by key and value, so one tenant's delete aimed
at the other's marker.

**The Prism client caches.** Three process-global caches of session-
authenticated clients, keyed on `namespace/name`. The second workspace to
reconcile a `default/demo` was handed the first's *authenticated session*:
wrong endpoint, wrong credentials, no error.

### Credentials are per cluster, or absent

The fourth is different in kind, and is the reason this page exists.

CAPX reads a cluster's credentials `Secret` through a `SharedInformerFactory`,
which is built over one clientset and addresses one API server. The informer
interface has nowhere to say which workspace is wanted —
`Lister().Secrets(ns).Get(name)` takes no context — so a fleet-wide controller
cannot use it correctly at all.

A reconciler may now be given a cluster-aware reader instead, resolving each
read against the cluster named in the context. Rather than fake the informer
interfaces, the fork implements the SDK's own environment `Provider`, which is
two methods.

More seriously, CAPX falls back to `/etc/nutanix/config/prismCentral` when a
`NutanixCluster` does not set `spec.prismCentral`. That file is the operator's
Prism Central and the operator's account, mounted into the manager's pod.
Serving one cluster that is a convenience. Serving many it is a way for any
tenant to obtain the operator's credentials by leaving a field out — not a
collision between tenants but an escalation past all of them, and a quiet one,
because the operator's credentials are perfectly valid credentials and the
cluster provisions.

**So there is no manager-level fallback here.** A `NutanixCluster` that names
no credentials is an error. The refusal is made twice: the endpoint builder
returns before the fallback branch, and the workspace helper's endpoint reader
is replaced with one that refuses, so the manager's credentials are
unreachable rather than merely unreached. The single-cluster path keeps its
fallback, which is why this is a condition rather than a removal.

## The three clients, and why none is interchangeable

The manager hands the reconcilers three cluster-aware clients. Each resolves
the workspace from the context of the call, and each covers a different way of
getting it wrong:

| | What it is for | What a local one would do |
|---|---|---|
| `Client` | the reconcile path's reads and writes | act on the wrong workspace's objects |
| `APIReader` | metro placement enumerating sibling `NutanixMachine`s | compute one tenant's placement from another tenant's machines |
| `CredentialReader` | reading Prism Central credentials | provision one tenant's cluster with another tenant's credentials |

`SetupWithMulticlusterManager` **refuses** to wire a reconciler that is missing
the last two, rather than defaulting them to the manager's. Every one of those
fallbacks is wrong in a way that succeeds: the cluster comes up, against the
wrong tenant's infrastructure.

## What has not been established

Everything above is verified by unit tests, by envtest, and by reading. **No
VM has been provisioned against a real Prism Central by this integration.** The
four fixes are proven at the seam — that the names differ, that the caches
separate, that the reader resolves per workspace, that the fallback is
refused — and not end to end. Whether two tenants can build clusters side by
side on one Prism Central is the run that would establish it, and it needs an
environment this project's CI does not have.
