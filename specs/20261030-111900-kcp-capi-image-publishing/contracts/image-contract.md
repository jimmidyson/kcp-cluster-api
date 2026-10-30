# Contract: Image Publishing

**Feature**: `001-kcp-capi-image-publishing`

## Images

Exactly 6 at v1. Each is `ko`'s build of one Go `main` package on its default Wolfi base image (no `gcr.io/distroless` override; override `defaultBaseImage` in `.ko.yaml` only if policy requires it later), running as `65532`.

| # | Target | Go importpath (`ko://…`) | Module dir | OCI name |
|---|--------|--------------------------|------------|---------|
| 1 | core | `github.com/jimmidyson/kcp-cluster-api/cmd/core-manager` | `.` | `ghcr.io/jimmidyson/kcp-capi-core` |
| 2 | kubeadm-bootstrap | `…/cmd/kubeadm-bootstrap-manager` | `.` | `ghcr.io/jimmidyson/kcp-capi-kubeadm-bootstrap` |
| 3 | kubeadm-control-plane | `…/cmd/kubeadm-control-plane-manager` | `.` | `ghcr.io/jimmidyson/kcp-capi-kubeadm-control-plane` |
| 4 | nutanix-infrastructure | `…/providers/nutanix-infrastructure/cmd/nutanix-infrastructure-manager` | `providers/nutanix-infrastructure` | `ghcr.io/jimmidyson/kcp-capi-nutanix-infrastructure` |
| 5 | workspace | `…/cmd/workspace-manager` | `.` | `ghcr.io/jimmidyson/kcp-capi-workspace` |
| 6 | dev-infrastructure | `…/cmd/dev-infrastructure-manager` | `.` | `ghcr.io/jimmidyson/kcp-capi-dev-infrastructure` |

Future providers add one row each (same `kcp-capi-<name>` prefix, flat, hyphen-separated).

## ko Configuration

File: `.ko.yaml` at repo root (with per-provider override at `providers/<name>/.ko.yaml` only if needed — R6).

No `defaultBaseImage` override: `ko`'s Wolfi default is used. Add one only if policy requires tracking distroless.

```yaml
builds:
  - id: kcp-capi-core
    main: ./cmd/core-manager
    ldflags: ["-s -w -X main.version={{.Env.REPO_VERSION}} -X main.gitCommit={{.Env.GIT_SHA}}"]
  # ... one per image above
```

Alternative: `baseImportPaths` + `KO_DOCKER_REPO=ghcr.io/jimmidyson` auto-mapping; the explicit `builds` form is preferred because it fixes the image names rather than deriving them from importpath suffixes.
## Labels (required on every image)

| Key | Value |
|-----|-------|
| `org.opencontainers.image.version` | `REPO_VERSION` (`vX.Y.Z` or `main`) |
| `org.opencontainers.image.revision` | Full `GITHUB_SHA` / `git rev-parse HEAD` |
| `org.opencontainers.image.source` | `https://github.com/jimmidyson/kcp-cluster-api` |
| `io.kcp-capi.capi-version` | `go list -m sigs.k8s.io/cluster-api` `.Version` (e.g. `v1.15.0-kcp.13`) |
| `io.kcp-capi.capi-base` | `DRIFT.md` `Base:` (upstream commit, e.g. `281e4e3…`) |
| `io.kcp-capi.k8s-version` | `go list -m k8s.io/api` `.Version` |
| `io.kcp-capi.capx-version` | `go list -m github.com/nutanix-cloud-native/cluster-api-provider-nutanix` (inside provider module) — Nutanix image only |

Provider images carry one extra provider-specific label; root images carry exactly the first six.

## Failure Modes

- `ko` invoked without `KO_DOCKER_REPO` → must error (wrapper script checks `KO_DOCKER_REPO` is set).
- Provider listed in `PROVIDER_MODULES` but missing `.ko.yaml` entry (when needed) → `task image:build` errors listing the missing importpath.
- Tag/label source (`go list -m`) fails (network, missing replace) → `scripts/image-tags.sh` errors before any `ko build` starts.
