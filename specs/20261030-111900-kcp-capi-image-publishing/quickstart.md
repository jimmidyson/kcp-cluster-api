# Quickstart: KCP CAPI Image Publishing

**Feature**: `001-kcp-capi-image-publishing` | **Branch**: `001-kcp-capi-image-publishing`

This guide proves the 6 published images work end-to-end: from local `ko` build through GHCR publish to a running KCP-hosted fleet.

## Prerequisites

- `go` 1.27.3 (from `go.mod`)
- `ko` (via `devbox shell` — `devbox.json` installs it; or `go install github.com/google/ko@latest`)
- Docker / `docker buildx` available (for `KO_DOCKER_REPO=ko.local` local builds)
- A kcp host cluster (local `kcp start --run-controllers` or an existing shard) for the integration validation
- `kubectl` + `task` (`task --version` ≥ 3.40)

## 1 — Local image build (no GHCR auth)

```bash
# Inside the feature worktree:
task image:build
# Equivalent raw ko call for one image:
# KO_DOCKER_REPO=ko.local ko build --bare --push=false ./cmd/core-manager --tags local

docker images | grep kcp-capi
# Expected: 6 entries like ko.local/kcp-capi-core:local, ko.local/kcp-capi-workspace:local, ...

# Verify labels encode the fork versions:
docker inspect ko.local/kcp-capi-core:local \
  --format '{{index .Config.Labels "io.kcp-capi.capi-version"}}'
# → v1.15.0-kcp.13  (must equal `go list -m sigs.k8s.io/cluster-api`)

scripts/image-tags.sh --check
# Exits 0 if every derived OCI tag matches ^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$, no '+' present.

task image:verify
# Builds (if needed) and asserts: each image's longTag's -capi- segment equals
# io.kcp-capi.capi-version label; provider images' extra segment matches capx version.
```

## 2 — Load into kind (dogfood the provider)

```bash
kind create cluster --name kcp-capi-smoke
task image:build
# For each of the 6, retag into kind:
for img in core kubeadm-bootstrap kubeadm-control-plane workspace dev-infrastructure; do
  kind load docker-image ko.local/kcp-capi-$img:local --name kcp-capi-smoke
done
# (nutanix needs a Prism mock or --dry-run flag in its manager; swap with a real endpoint when available)

# Deploy via the project's existing manifest path (once added):
# kubectl apply -f config/deploy/kcp-capi/   (or helm/kustomize overlay)
kubectl get pods -A  # all 6 Deployments Ready
```

## 3 — Multi-workspace fleet validation (P1 acceptance)

This is the `task demo` equivalent but driven from published/local images rather than `go run`.

```bash
# With the 6 images running as Deployments in the kcp host cluster:
# 1. Publish the 6 APIExports (already in internal/capiexports):
go run ./cmd/capiexports --publish  # (or however the deployment does it)
# 2. Create two tenants, each with a Workspace of type cluster-api:
#    → assert WorkspaceType binding produces ClusterRoles + capiexport APIBindings
# 3. In each workspace, create a ClusterClass and a Cluster:
#    → assert both reach Cluster phase Available, Machines provisioned,
#      different CAPI categories, no shared Prism cache/credentials bleed.

task verify   # still the done-condition: includes test:integration against real kcp
```

## 4 — CI publish path (what happens on release)

No manual run; documented for review. On `vX.Y.Z` + `providers/nutanix-infrastructure/vX.Y.Z` tag push (created by release-please merge):

1. `publish-images.yaml` GHA triggers on `push: tags: ['v*']`.
2. Logs in to `ghcr.io` via `docker/login-action` (`GITHUB_TOKEN` has `packages: write`).
3. Calls `scripts/image-tags.sh --gha-output` to emit `REPO_VERSION`, `CAPI_VERSION`, `CAPX_VERSION`, `K8S_VERSION`, `CAPI_BASE`, and the per-image `TAGS` matrix into `GITHUB_OUTPUT`.
4. For each image, `ko build --bare --push --platform=linux/amd64,linux/arm64` with `--tags <repoTag>,<longTag>,<forkTag>` and `--image-label io.kcp-capi.*`.
5. On `push: branches: [main]` a separate job pushes `:main` and `:sha-<short>`.

## 5 — Adding a new provider (contract check)

```bash
# 1. Scaffold:
mkdir -p providers/aws/cmd/aws-manager
# + go.mod (replace kcp-cluster-api => ../.., restated CAPI replaces), main.go, DRIFT.md, optional .ko.yaml

# 2. Register:
#    Taskfile.yaml:  PROVIDER_MODULES: "providers/nutanix-infrastructure providers/aws"
#    release-please-config.json: packages["providers/aws"] = {"package-name": "aws"}
#    scripts/image-tags.sh:  add one row mapping kcp-capi-aws → go list -m <aws importpath>
#    .ko.yaml: add one builds entry for the new importpath

# 3. Prove it:
task build          # now iterates the new module
task drift          # checks providers/aws/DRIFT.md
task image:build    # produces ghcr.io/jimmidyson/kcp-capi-aws locally
task image:verify   # tag derivation includes the new provider's upstream
```

| Check | Pass indicator |
|-------|---------------|
| `task image:build` | 6 images in `docker images`, each `<150 MB` compressed, `ko` Wolfi base |
| `scripts/image-tags.sh --check` | exit 0, no `+` in any derived tag, all match OCI regex |
| `task image:verify` | exit 0; every longTag's embedded fork version equals the image label |
| `ghcr.io/jimmidyson/kcp-capi-core:vX.Y.Z` pull | same digest as `:vX.Y.Z-capi-…` and `:<forkTag>`; labels show correct pin |
| Adding fixture provider | ≤4 doc edits, `task build` + `task image:build` pick it up, no redesign |
| Existing `v0.1.0` / `bootstrap-sha` | `git tag --list` still shows them; `go get ...@v0.1.0` resolves |
