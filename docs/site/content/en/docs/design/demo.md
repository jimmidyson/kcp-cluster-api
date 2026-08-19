---
title: The Demo
description: What the one-command demo demonstrates, what it deliberately leaves out, and the two environment facts it had to learn.
weight: 28
---

`task demo` builds Cluster API clusters across several kcp workspaces from
one manager, and waits for them to be ready. This page is why it exists in the shape it does. For how to run
it, see [Demo](../user/demo.md).

## Why a demo is a deliverable

Everything this project does end to end was, until this existed, only
observable by reading an integration test. That is a real gap rather than a
presentational one: a system whose behaviour can only be seen through `go test`
cannot be shown to anyone deciding whether to use it, and cannot be poked at by
whoever is about to change it.

So the demo is not a script that reimplements the manager. `internal/demo`
calls `coremanager.SetupFleetControllers` — the same function
`cmd/core-manager` calls, with the same options — because a demo of a
reimplementation demonstrates the reimplementation. The same holds for each of
the other three providers' setup functions.

## What it asserts, and where

The demo prints a table; the assertions live in `test/integration/demo`, which
`task verify` runs. Both drive `internal/demo.Run`, so a demo that breaks fails
CI rather than being discovered at the next presentation.

The test asserts two things:

1. **Every workspace's cluster is ready** — the `Cluster`'s `Available`
   condition, every control plane replica it was asked for, and every `Machine`
   Ready — driven by one manager that was told about no workspace. Each is
   engaged because its `APIBinding` became ready.
2. **No workspace's objects are another's.** Each workspace sees exactly one
   `Cluster`, the `Cluster`s are distinct objects, and each `DevCluster` is
   owned by the `Cluster` in its own workspace.

The second is the conversion plan's P8, and it is the one the rest of the suite
never made:
[`TestEveryBoundWorkspaceIsWired`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/test/integration/providerwiring/providerwiring_test.go)
exercises many workspaces without reconciling any of them to completion, and
`TestCoreManagerClusterToMachine` reconciles one workspace's cluster without
there being a second. Neither could have caught a reconciler writing one
workspace's status into another.

Identical object names in every workspace are load-bearing for that assertion.
A leak between two workspaces each holding a `demo-00` cannot hide behind names
that happen not to collide.

## Why it waits for ready rather than provisioned

It did wait for provisioned, back when a `Machine` reaching Ready needed a
bootstrap provider and a control-plane provider and neither was wired. Both are
wired now (the conversion plan's P1 and P2), so the reason to stop early is
gone — and stopping early was never free. Provisioned infrastructure, an
initialized control plane and a bootstrap data secret are all true of a cluster
whose machines never go Ready, which is the shape every bug in this wiring has
had: a missing `spec.providerID` index meant no `Machine` ever got a `nodeRef`,
and the demo reported that run as a success.

So the done-condition is readiness, and the table reports the milestones
alongside it rather than instead of it. A run that does not finish says which
condition it is still waiting on.

## What it leaves out, and why

**Webhooks.** They are served for one workspace or none until the webhook
dispatch layer (G4) lands, so a multi-workspace demo cannot use them. Two
consequences follow, and both are visible in the code: every object it creates
is fully specified, because nothing defaults it, and every published type is
trimmed to its storage version, because a multi-version type needs a conversion
strategy and a conversion strategy needs a webhook server.

## Two things about the environment that had to be learned

Both cost a debugging session, and both are the kind of thing that is invisible
until it is not.

**Every URL kcp hands out is pinned to `localhost`.** A kcp server advertises
the address it detects for itself, and for a server started on this machine by
this process that address is wrong twice over: it need not be routable from
here, and where an `HTTPS_PROXY` is set — as in this project's own sandboxes,
and most corporate networks — client-go sends the connection to the proxy
instead of to the local port. Three URLs matter and only the first is visible
in the kubeconfig: the shard's base URL, its external URL, and the **virtual
workspace URL**, which is what ends up in the `APIExportEndpointSlice` and so
is the address the manager itself connects to. Pinning the first two and
leaving the third undoes the fix, and the failure arrives halfway through
wiring the reconcilers as a reset connection with nothing about proxies in it.

**Readiness is three events, not one.** `/readyz` goes green first, and the
root workspace does not exist yet when it does. The root workspace's
`LogicalCluster` appears next, and `tenancy.kcp.io` is still absent from that
workspace's discovery. Waiting on either of the first two produces a failure
with a misleading shape: a controller-runtime client caches discovery on first
use and rate-limits reloading it, so a client built in that window fails its
first `Workspace` call with `no matches for kind "Workspace"` — which reads
like a scheme registration bug and is not. The demo waits for the API it is
about to use, and retries the first create on top of that.
