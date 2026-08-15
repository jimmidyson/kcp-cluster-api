---
title: Installation
description: Prerequisites and how to build kcp-cluster-api.
weight: 10
---

{{% pageinfo color="info" %}}
kcp-cluster-api is early-stage: the KCP-aware manager entrypoint under
The workspace-aware manager is still a walking skeleton, so today this repository builds and behaves
like standard upstream [Cluster API](https://cluster-api.sigs.k8s.io/). This
page will grow a dedicated "install the KCP-aware manager" section once that
lands — see [Design & architecture](../design/_index.md) for the integration plan.
{{% /pageinfo %}}

## Prerequisites

- Go (see the repository's root `go.mod` for the required version)
- [Docker](https://www.docker.com/) or another OCI-compatible container
  runtime, for building images and running the local
  [Tilt](https://tilt.dev/)-based development environment
- [kustomize](https://kustomize.io/) and [kubectl](https://kubernetes.io/docs/tasks/tools/)
- A Kubernetes cluster to install into (for local development, the `Tiltfile`
  drives a [kind](https://kind.sigs.k8s.io/) cluster for you)
- A running [KCP](https://github.com/kcp-dev/kcp) instance, once KCP-aware
  components are available

## Get the source

```sh
git clone https://github.com/jimmidyson/kcp-cluster-api.git
cd kcp-cluster-api
```

## Build

The root `Makefile` (unmodified upstream Cluster API tooling) drives builds:

```sh
# Build all manager binaries.
make managers

# Build and push controller images.
make docker-build docker-push
```

Run `make help` for the full list of targets.

## Local development with Tilt

The fastest way to iterate is the upstream `Tiltfile`, which spins up a
[kind](https://kind.sigs.k8s.io/) cluster, builds images, and live-reloads
controllers on save:

```sh
make tilt-up
```

See the upstream
[Cluster API developer guide](https://cluster-api.sigs.k8s.io/developer/getting-started)
for details — none of that tooling is modified by this fork.

## Installing with clusterctl

Because upstream code is unmodified, `clusterctl` and the standard Cluster API
manifests work exactly as they do upstream. See the
[Cluster API quickstart](https://cluster-api.sigs.k8s.io/user/quick-start)
for the general flow; KCP-specific installation steps will be documented here
once the workspace-aware components are complete.
