# Contract: Tag & Manifest Schema

**Feature**: `001-kcp-capi-image-publishing`

## OCI Tag Grammar

All tags match `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$` and contain no `+`. Go semver `+build` metadata (from `+incompatible` or `-kcp.N` pseudo-versions) has `+` replaced with `-` for OCI.

## Per-Release Tag Matrix (same digest, three aliases)

Root images (1,2,3,5,6 — pinned to CAPI fork):

```
<repoVersion>                          # deployment pin  e.g. v0.2.0
<repoVersion>-capi-<capiVersion>       # provenance      e.g. v0.2.0-capi-v1.15.0-kcp.13
<capiVersion>                          # fork alias       e.g. v1.15.0-kcp.13
```

Provider images (4 — pinned to provider fork AND CAPI fork):

```
<repoVersion>                                         # same deployment pin
<repoVersion>-<provider>-<providerVersion>-capi-<capiVersion>   # e.g. v0.2.0-capx-v1.11.0-kcp.3-capi-v1.15.0-kcp.13
<providerVersion>                                    # provider fork alias e.g. v1.11.0-kcp.3
```

where `<provider>` is the short lowercase upstream name (`capx` for Nutanix, `capa` for a future AWS provider) defined in the image table; long-tag length is validated `< 100` (hard OCI limit 128).

## Branch Tags

On every push to `main` (publish workflow path):

```
main          # floating
sha-<short>   # e.g. sha-a1b2c3d (7 hex from GITHUB_SHA)
```

Release tags do not get these; `main` tags do not get the 3-alias matrix.

## Derivation (mechanical, no hand-typed versions)

```bash
REPO_VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0-dev)"
CAPI_VERSION="$(go list -m -f '{{.Version}}' sigs.k8s.io/cluster-api)"
# inside providers/nutanix-infrastructure:
CAPX_VERSION="$(go list -m -f '{{.Version}}' github.com/nutanix-cloud-native/cluster-api-provider-nutanix)"
K8S_VERSION="$(go list -m -f '{{.Version}}' k8s.io/api)"
CAPI_BASE="$(sed -n 's/^Base: *//p' DRIFT.md | awk '{print $1}')"
```

`REPO_VERSION`, `CAPI_VERSION`, etc. are then sanitized: `oci_sanitize() { tr '+' '-' ; }`.

The script `scripts/image-tags.sh` implements this, invoked as:

- Locally: `scripts/image-tags.sh --check` (derive + validate, print table, exit non-zero on violation).
- In GHA: `scripts/image-tags.sh --gha-output` (same + `echo "tags=$TAGS" >> $GITHUB_OUTPUT`).

No version literal appears in `Taskfile.yaml` or `.github/workflows/publish-images.yaml`; they read the script's outputs.

## Go Tag ↔ OCI Tag Correspondence

| Git tag (Go module) | OCI tag | Notes |
|---------------------|---------|-------|
| `vX.Y.Z` (root) | `vX.Y.Z` (all 6 images) | One commit, all 6 aliases |
| `providers/<name>/vX.Y.Z` | same `vX.Y.Z` (all 6) | Lockstep: provider module tag equals root tag |
| — | `vX.Y.Z-capi-…` | Provenance, not a Git tag |
| — | `<forkVersion>` | Upstream fork alias, not a Git tag in this repo |

Verification:
- `go get github.com/jimmidyson/kcp-cluster-api@vX.Y.Z` resolves (root tag exists).
- `go get .../providers/nutanix-infrastructure@vX.Y.Z` resolves (nested tag exists at same commit).
- `docker manifest inspect ghcr.io/jimmidyson/kcp-capi-core:vX.Y.Z` and `:vX.Y.Z-capi-…` and `:<forkVersion>` share `config.digest`.
- Each image's `io.kcp-capi.capi-version` label equals the `-capi-` segment of its long tag.

## release-please Config

```json
{
  "$schema": "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json",
  "release-type": "go",
  "include-component-in-tag": false,
  "bootstrap-sha": "281e4e3ed2af1d6852651d69e1207a3073b478c2",
  "initial-version": "0.1.0",
  "changelog-type": "github",
  "pull-request-title-pattern": "release${scope}:${component} ${version}",
  "bump-minor-pre-major": true,
  "bump-patch-for-minor-pre-major": false,
  "packages": {
    ".": { "package-name": "kcp-cluster-api" },
    "providers/nutanix-infrastructure": { "package-name": "nutanix-infrastructure" }
  },
  "changelog-sections": [ /* unchanged */ ]
}
```

`.release-please-manifest.json`:

```json
{
  ".": "0.1.0",
  "providers/nutanix-infrastructure": "0.1.0"
}
```

Invariants: `include-component-in-tag` stays `false` (Go `path/vX.Y.Z` form); `bootstrap-sha` and `initial-version` are untouched (FR-010).
