# Drift record: the Nutanix infrastructure provider

What this project carries against upstream CAPX, and why. It is the second
record of its kind — see the root [`DRIFT.md`](../../DRIFT.md) for Cluster
API's, and the reasoning both are governed by.

`task drift` checks this file against reality and fails on any path that
diverges without an entry here.

Fork: [`github.com/jimmidyson/cluster-api-provider-nutanix`](https://github.com/jimmidyson/cluster-api-provider-nutanix),
branch `kcp/v1.11`, tag `v1.11.0-kcp.2`

> One tag, not three. CAPX is a single Go module, where Cluster API is three —
> the "three tags per release" rule in the root record is a fact about
> Cluster API's `api/` and `test/` submodules rather than about forks.
>
> The pins do not propagate, and there are two of them: this module and the
> repository root both pin the fork, because Go honours `replace` only in the
> main module. They must move together — a pair that disagreed would build two
> different CAPX into one repository.

Base: `0b2405dc5eae5fbb5d810ba16d40f64098e05a47`

The base is an upstream `main` commit past `v1.10.3`, which is why the branch
is `kcp/v1.11`: it names the release line that commit becomes, as `kcp/v1.15`
does for Cluster API.

## What kind of divergence this is

All of it is **carried deliberately**, in the sense the root record means: the
workspace-aware wiring is not proposed upstream, per
[ADR-0003](../../docs/adr-0003-workspace-aware-cluster-api.md) and
[ADR-0004](../../docs/adr-0004-scaling-to-many-provider-forks.md).

Four entries are marked **upstreamable** rather than kcp-specific, and the
distinction is not cosmetic. Cluster and Machine names are namespace-scoped in
Cluster API, so the collisions they fix happen between two *namespaces* of one
ordinary management cluster, with no kcp involved. Nothing has been filed; if
any of them lands upstream it stops being drift.

## Carried patches

| Path | Rationale | Upstream |
|---|---|---|
| `api/v1beta1/nutanix_types.go` | **Modified.** Adds the `kcp.io/cluster` annotation key and the accessor that reads it. It lives in the API package because two packages need it and neither can import the other: the controllers derive resource names from it, and `pkg/client` keys its Prism client caches on it. | None — deliberate |
| `controllers/logicalcluster.go` | New file. Qualifies a name by the logical cluster its `Cluster` was read from. Empty means do not qualify, so a CAPX outside kcp names things exactly as before — asserted, not assumed. | None — deliberate |
| `controllers/logicalcluster_test.go` | New file. That two workspaces holding a same-named `Machine` cannot name one VM, that an absent logical cluster changes nothing, and that a nil `*Cluster` is unscoped rather than a panic. | None — deliberate |
| `controllers/nutanixmachine_controller.go` | **Modified.** VM names are qualified at both the create and delete sites. A VM was named for its `Machine` and looked up by a Prism-wide `name eq` query that refuses to resolve once two share a name, so two workspaces broke each other's reconcile rather than colliding quietly. Also carries the credential source. | **Upstreamable** — two namespaces collide the same way |
| `controllers/helpers.go` | **Modified.** The CAPI category is qualified, and the two credential-resolving functions take a `credentialSource` rather than raw informers. The category marks what CAPX owns and was keyed on cluster name alone, so two workspaces addressed one — and teardown deletes by key and value, aiming one tenant's delete at the other's marker. | **Upstreamable** — the category half |
| `controllers/helpers_test.go` | **Modified.** Call shape only, plus a test that two workspaces' categories separate. | **Upstreamable** — with the change it covers |
| `controllers/credentials.go` | New file. `credentialSource`: reader or informers, named rather than passed as two more arguments, because handing a fleet-wide reconciler the wrong one provisions a cluster with another tenant's credentials instead of failing. | None — deliberate |
| `controllers/nutanixcluster_controller.go` | **Modified.** Carries `CredentialReader` and the `credentials()` accessor, and passes the logical cluster to the category identifiers. | None — deliberate |
| `controllers/nutanixvirtualhadomain_controller.go` | **Modified.** As above. It has no fleet-wide setup yet, so its reader is nil and it uses the informer path unchanged. | None — deliberate |
| `controllers/nutanixmachine_controller_test.go` | **Modified.** Call shape only. | None — deliberate |
| `controllers/nutanixcluster_controller_workspace.go` | New file. `SetupWithMulticlusterManager` for `NutanixCluster`: the same wiring with the fleet-wide builder, the mappers built against the reconciler's cluster-aware client, and the scheme from the local manager. | None — deliberate, ADR-0003 |
| `controllers/nutanixmachine_controller_workspace.go` | New file. As above for `NutanixMachine`, and it **requires** `APIReader` where its single-cluster twin defaults it: metro placement enumerates sibling `NutanixMachine`s through that reader, so the manager's would compute one tenant's placement from another tenant's machines. | None — deliberate, ADR-0003 |
| `pkg/client/cache.go` | **Modified.** The three process-global caches of session-authenticated Prism clients keyed on `namespace/name`. The second workspace to reconcile a `default/demo` was handed the first's authenticated session — wrong endpoint, wrong credentials, no error. The key now carries the logical cluster. | None — deliberate |
| `pkg/client/cache_test.go` | New file. That two workspaces' identically named clusters do not share a cache key, and that one workspace's entry is not returned to another. | None — deliberate |
| `pkg/client/client.go` | **Modified.** `NewWorkspaceHelper` reads credentials through a cluster-aware client instead of informers, and **refuses the manager's own credentials** when serving many tenants. See below. | None — deliberate |
| `pkg/client/workspaceprovider.go` | New file. The SDK's environment `Provider`, implemented over a controller-runtime client. The SDK's own Kubernetes provider reads through a `SharedInformerFactory`, whose interface takes no context, so it cannot say which workspace's Secret is wanted. | None — deliberate |
| `pkg/client/workspaceprovider_test.go` | New file. That two workspaces' identically named credentials Secrets resolve differently, that a missing Secret is a named error, that the manager's credentials are refused, and — separately — that the file reader is unreachable in workspace mode. | None — deliberate |
| `pkg/client/client_test.go` | **Modified.** Call shape only. | None — deliberate |
| `pkg/context/context.go` | **Modified.** Removes `GetRemoteClient`, `RemoveRemoteClient` and `RemoteClientCache`. The cache was keyed on `ObjectKey` and would have handed one workspace another's workload-cluster client, but nothing populated it: `GetRemoteClient` had no callers and was the only writer. Removing it also fixed the one compile error the Cluster API bump caused, since it was the only user of the removed `remote.NewClusterClient`. | **Upstreamable** — dead code either way |
| `go.mod` | **Modified.** Cluster API pinned at this project's fork, and the runtime raised to match: controller-runtime v0.24, `k8s.io` v0.36. | None — deliberate |
| `go.sum` | **Modified.** As above. | None — deliberate |

## Credentials are per cluster, or absent

The entry above understates this one, so it gets its own section.

CAPX falls back to `/etc/nutanix/config/prismCentral` when a `NutanixCluster`
does not set `spec.prismCentral`. That file is the operator's Prism Central and
the operator's account, mounted into the manager's pod.

Serving one cluster that is a convenience. Serving many it is a way for any
tenant to obtain the operator's credentials by leaving a field out — not a
collision between tenants but an escalation past all of them, and a quiet one,
because the operator's credentials are perfectly valid credentials and the
cluster provisions.

So there is no manager-level fallback in workspace mode, and the refusal is
made twice: the endpoint builder returns before the fallback branch, and the
workspace helper's endpoint reader is replaced with one that refuses. The
manager's credentials are unreachable rather than merely unreached, so
reordering those branches later cannot quietly hand them out. The
single-cluster path keeps its fallback, which is why this is a condition
rather than a removal.

## What is not covered

The fork is verified by unit tests, by envtest, and by reading. **No VM has
been provisioned against a real Prism Central**, so every entry above is
proven at the seam and none of it end to end. CAPX's own e2e suite needs a
live Prism Central, which this project's CI does not have.
