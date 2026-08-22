# Feature Specification: A workspace-aware UI for the demo

**Feature Branch**: `claude/headlamp-capi-workspace-nav-x86rl5`

**Created**: 2026-08-22

**Status**: Shipped in [#106](https://github.com/jimmidyson/kcp-cluster-api/pull/106)

**Input**: User description: "I want to include headlamp in the demo with the capi plugin enabled so users can view their capi resources in headlamp. I want the user to be able to move to different workspaces in the headlamp UI. [...] show that when I switch between workspaces the capi plugin is only enabled in workspaces where the capi API binding exists [...] strip non kcp resources such as pods from the sidebar [...] ensure the capi plugin doesn't list CRDs and doesn't error on missing provider versions, instead using API discovery for both."

## Purpose

`task demo` proves that one manager serves Cluster API across many workspaces,
and it proves it in a terminal. Everything the demo demonstrates - that a
workspace's objects stay its own, that a tenant sees theirs and nobody else's,
that a workspace serves Cluster API because somebody bound it - is visible
only to a reader who already knows what to run.

A UI is the second audience for the same claims, and a workspace is the
awkward part: every tool that talks to Kubernetes assumes the cluster it is
pointed at serves Kubernetes' APIs. A kcp workspace serves what its
`APIBinding`s bind, which is a smaller and different set in every workspace,
and the difference is not a detail to be papered over. It is the thing being
demonstrated.

This feature makes that difference legible: moving between workspaces in the
UI, and seeing the UI itself change with the workspace.

## Scope

**In scope**

1. The demo writes a kubeconfig per audience - the operator's, holding the
   whole tree, and one per tenant holding theirs - each with one context per
   workspace.
2. A Headlamp plugin - a separate repository - that navigates the workspace
   tree, hides what the workspace does not serve, and shows a plugin's
   section only in workspaces whose bindings back it.
3. Changes to the upstream Cluster API Headlamp plugin so that it detects
   Cluster API by discovery rather than by reading
   `CustomResourceDefinition`s, carried as a patch against a pinned upstream
   commit until they land upstream.
4. User documentation for running the demo with a UI.

**Out of scope**

- Shipping, packaging or vendoring Headlamp itself. The demo assumes a
  Headlamp the operator already has.
- Writing to workspaces from the UI. Everything here is read-only navigation.
- Any change to how the managers work.

## User scenarios

### A person meeting the project

**Given** `task demo` has run with `--wait`, **when** they point Headlamp at
the kubeconfig it wrote, **then** the cluster chooser lists the demo's
workspaces by path, and choosing one shows that workspace.

### Moving around the tree

**Given** they are viewing `root:capi-demo:alice`, **when** they open the
workspace navigator, **then** they see the workspaces inside it, what each was
created with, and which plugins each one lights up - **and** choosing one
switches to it.

### The same UI, a different workspace

**Given** they are viewing a workspace that binds the Cluster API
`APIExport`, **then** the Cluster API section is in the sidebar and shows the
workspace's `Cluster`s and `Machine`s. **When** they move one workspace up,
where nothing binds it, **then** that section is gone, along with its routes.

### A UI that does not offer what is not there

**Given** any workspace, **then** the sidebar offers no Pods, Deployments or
Nodes, because no workspace serves them, **and** a heading whose entries have
all gone is gone too.

## Functional requirements

- **FR-001** A demo run writes one kubeconfig holding every workspace it
  created as the admin, and one per tenant holding that tenant's home and
  workspaces. Contexts are named after the workspace path.
- **FR-002** A tenant's workspaces are browsed as that tenant. The workspaces
  above them are in the operator's file only, because nothing grants a tenant
  anything there.
- **FR-003** A tenant's file offers no route into another tenant's
  workspaces, not even a context that would be refused: a UI cannot enter a
  workspace it is refused, so it asks for a login token instead, which reads
  as "you are not signed in" rather than "this is not yours".
- **FR-003a** Being another tenant is loading that tenant's file. No file
  offers a choice of tenant, because a UI shows what it was given and a
  chooser listing both would make identity a menu item.
- **FR-004** Credentials are copied into the generated kubeconfig, so it
  stands alone.
- **FR-005** The plugin determines what a workspace serves from discovery,
  and never from `CustomResourceDefinition`s.
- **FR-006** A sidebar entry for a resource the workspace does not serve is
  hidden; a heading with no surviving entries is hidden.
- **FR-007** A gated section - a sidebar entry plus the API groups behind it
  - appears only in workspaces serving one of those groups, and its routes
  are removed elsewhere. Gates are configuration, not code.
- **FR-008** When discovery has not answered, or failed, nothing is hidden.
- **FR-009** The Cluster API plugin detects Cluster API, and which version to
  request, from discovery.
- **FR-010** The Cluster API plugin reports no error, and shows no "could not
  be loaded" row, in a workspace where the types exist without CRDs.

## Success criteria

- **SC-001** In a demo workspace bound to Cluster API, the Cluster API
  section lists the demo's cluster and machines.
- **SC-002** In the workspace above it, that section is absent.
- **SC-003** No workspace offers Pods, Nodes or Workloads.
- **SC-004** Switching workspace is one click from the navigator, and the
  navigator says which plugins a workspace lights up before it is opened.

## Verification

`internal/demo`'s unit tests cover the generated kubeconfig. The plugin's own
tests cover the gating. The end-to-end claim - the UI changing with the
workspace - is checked by driving a browser against a live demo, and is
recorded in `evidence/`.
