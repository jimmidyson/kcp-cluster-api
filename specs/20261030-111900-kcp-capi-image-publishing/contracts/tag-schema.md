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

## No-Fork-Bump Contract (T022)

When a repo release bumps `vX.Y.Z → vX.Y.Z+1` but the CAPI/CAPX pins are
unchanged between the two commits (no `go.mod` change to
`sigs.k8s.io/cluster-api` or
`github.com/nutanix-cloud-native/cluster-api-provider-nutanix`):

- The derived `forkTag` (`v1.15.0-kcp.13` or `v1.11.0-kcp.3`) is identical for
  both releases.
- The derived OCI labels (`io.kcp-capi.capi-version`, `io.kcp-capi.k8s-version`,
  `io.kcp-capi.capi-base`, and for provider images
  `io.kcp-capi.capx-version`) are identical — they come from the same
  `go list -m` values.
- Only `repoTag` (`vX.Y.Z → vX.Y.Z+1`) and the prefix of `longTag`
  (`vX.Y.Z-capi-… → vX.Y.Z+1-capi-…`) differ; the suffix (`-capi-v1.15.0-kcp.13`)
  is the same.
- The `forkTag` alias (`:<capiVersion>`) is a **floating alias** — `ko` pushes
  with `--tags repo,long,fork`, so the new digest overwrites the old alias.
  After the release, `:v1.15.0-kcp.13` points at the newest digest. No history
  tag or retention is needed; consumers pinning the fork version always get
  the latest security-fix digest.

  The implementation is in `scripts/image-tags.sh`: `forkTag` is the
  OCI-sanitized `CAPI_VERSION`/`CAPX_VERSION` from `go list -m` (not a separate
  counter), and the publish workflow runs `ko build --push --tags repo,long,fork`
  which upserts all three. Labels come from the same `go list -m` values via
  Taskfile `--image-label` passthrough, so unchanged pins give identical labels.

  Verification: `scripts/image-tags.sh --check` prints the three tags per image
  and validates uniqueness; `task image:verify` asserts `longTag`'s `-capi-`
  segment equals the `io.kcp-capi.capi-version` label (SC-002), so a no-fork-bump
  release is verified by the same path as any other.



## Edge case: mismatched Go tags (T037)

`providers/nutanix-infrastructure/go.mod` has `replace kcp-cluster-api => ../..`, so
`providers/nutanix-infrastructure/vX.Y.Z` and `vX.Y.Z` (root) must be created at the
**same commit** (FR-006). Building from a commit that has only one of the two tags —
e.g. checked out at `v0.2.0` but `providers/nutanix-infrastructure/v0.2.0` missing —
must fail loudly, not resolve a stale dependency. Enforcement:

- `release-please` creates both tags atomically on merge of the release PR; out-of-band
  tag creation is not supported.
- `scripts/image-tags.sh --check` derives `CAPI_VERSION`/`CAPX_VERSION` from `go list -m`
  at that commit; a checkout missing the provider tag will have its `go.mod` replace
  pointing at a non-existent version and `go list -m` will error, causing `--check`
  (and thus `task image:build`/`image:verify`) to fail.
- `task image:check-modules` ensures `PROVIDER_MODULES` covers all provider modules;
  an untagged module without a `task drift` entry is flagged.

## Verification expectations (T036)

- `task drift` must pass after dual-package changes (preserves `bootstrap-sha`
  `281e4e3ed2af1d6852651d69e1207a3073b478c2`, `initial-version` `0.1.0`,
  `bump-minor-pre-major: true`; adds `providers/nutanix-infrastructure` package).
  Existing `v0.1.0` tag history remains resolvable.
- `task verify` (and `task check`) remain green; image publish does not add a
  required verify step. `image:verify`'s label inspection is skipped when no
  docker/buildah/podman daemon is present (documented as a CI-only assertion
  in `.github/workflows/publish-images.yaml`); this is not a "could not run"
  that weakens `task verify` — it is a separate task.
- In envs without a `kcp` fixture (e.g. `go test -tags=integration` skipped),
  `task verify`/`task check` may report `could not run` for container-runtime
  steps — this is **not** a failure; document the skip and record the
  `bin/verify-result.json` outcome. See `AGENTS.md#Done is a command`.

Gaps recorded during `quickstart.md` §3–§5 validation are listed in
`specs/20261030-111900-kcp-capi-image-publishing/quickstart.md` under
`## Validation gaps` (added during T038).
