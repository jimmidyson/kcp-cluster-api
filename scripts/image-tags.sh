#!/usr/bin/env bash
set -euo pipefail

# Derive image tags from git + go module graph.
# Usage:
#   scripts/image-tags.sh                 # print human-readable table
#   scripts/image-tags.sh --check         # validate only, no output
#   scripts/image-tags.sh --gha-output    # write to $GITHUB_OUTPUT for CI
#   scripts/image-tags.sh --repo-version  # print REPO_VERSION only (for --help smoke)
#   scripts/image-tags.sh --capi-version  # print the CAPI upstream component only
#   scripts/image-tags.sh --capx-version  # print the CAPX upstream component only
#
# Environment overrides:
#   REPO_VERSION, CAPI_VERSION, CAPX_VERSION, K8S_VERSION, CAPI_BASE
#   — set any to skip the corresponding derivation.
#
# Tag format (contracts/tag-schema.md#per-release-tag-matrix):
#   Root images:    <repo>               e.g. v0.2.0
#                   <repo>-capi-<capi>   e.g. v0.2.0-capi-281e4e3ed2af
#                   <capi>               e.g. 281e4e3ed2af  (upstream alias)
#   Provider images: <repo>                              e.g. v0.2.0
#                    <repo>-capx-<capx>-capi-<capi>     e.g. v0.2.0-capx-b3b16750a6f1-capi-281e4e3ed2af
#                    <capx>                               e.g. b3b16750a6f1
#   Every version component is OCI-sanitized (+→-). The alias is the upstream
#   component itself — a floating alias that ko overwrites on every release.
#
# Where <capi> and <capx> come from: the upstream trees are vendored under
# third_party/forked/ (ADR-0005), so there is no fork tag to read from the module graph any
# more — `go list -m` reports a directory and a placeholder version. What
# identifies the upstream each tree is cut from is the base commit its drift
# record names, so that is the component: the first twelve characters of
# `Base:` in DRIFT.md and providers/nutanix-infrastructure/DRIFT.md. It is
# the one identifier of the upstream a tree is cut from that is derivable
# offline from the record alone, it keeps every tag unique, and the checks
# below assert both. See contracts/tag-schema.md, amended for ADR-0005.
#
# Unchanged bases reuse the same alias/labels; ko push is an upsert, so the
#   new digest overwrites the old alias and `:<capi>` always points at the
#   newest release. No extra retention or history tag is needed.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

MODE="print"
if [[ "${1:-}" == "--check" ]]; then MODE="check"
elif [[ "${1:-}" == "--gha-output" ]]; then MODE="gha"
elif [[ "${1:-}" == "--repo-version" ]]; then MODE="repo-version"
elif [[ "${1:-}" == "--capi-version" ]]; then MODE="capi-version"
elif [[ "${1:-}" == "--capx-version" ]]; then MODE="capx-version"
elif [[ "${1:-}" == "--help" ]]; then echo "Usage: $0 [--check|--gha-output|--repo-version|--capi-version|--capx-version]"; exit 0
fi

derive() {
  local repo_version="${REPO_VERSION:-}"
  local capi_version="${CAPI_VERSION:-}"
  local capx_version="${CAPX_VERSION:-}"
  local k8s_version="${K8S_VERSION:-}"
  local capi_base="${CAPI_BASE:-}"

  if [[ -z "$repo_version" ]]; then
    if [[ -n "${GITHUB_REF_NAME:-}" ]] && [[ "$GITHUB_REF_NAME" == v* ]]; then
      repo_version="$GITHUB_REF_NAME"
    else
      repo_version="$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0-dev)"
    fi
  fi

  # The upstream component of a tag is the base commit the tree's drift record
  # names (see the header). Read once here; capi_base below is the same value
  # at full length, kept for the OCI label.
  base_of() {
    sed -n 's/^Base: *`//p' "$1" 2>/dev/null | sed 's/`.*$//' | tr -d '[:space:]'
  }
  if [[ -z "$capi_version" ]]; then
    capi_version="$(base_of "$REPO_ROOT/DRIFT.md")"
    capi_version="${capi_version:0:12}"
    if [[ -z "$capi_version" ]]; then capi_version="unknown"; fi
  fi
  if [[ -z "$capx_version" ]]; then
    # For a new provider, add a sibling block reading that provider's record.
    capx_version="$(base_of "$REPO_ROOT/providers/nutanix-infrastructure/DRIFT.md")"
    capx_version="${capx_version:0:12}"
    if [[ -z "$capx_version" ]]; then capx_version="unknown"; fi
  fi
  if [[ -z "$k8s_version" ]]; then
    k8s_version="$(go -C "$REPO_ROOT" list -m -f '{{.Version}}' k8s.io/api 2>/dev/null || echo unknown)"
  fi
  if [[ -z "$capi_base" ]]; then
    capi_base="$(base_of "$REPO_ROOT/DRIFT.md")"
    if [[ -z "$capi_base" ]]; then capi_base="unknown"; fi
  fi

  # Sanitize + → - for OCI tag charset (tag-schema.md#oci-tag-grammar).
  # Go pseudo-versions may contain +incompatible or +build metadata which OCI
  # forbids. We sanitize every version-derived tag component, including forkTag
  # itself — a forkTag with '+' would fail ko push.
  local capi_oci capx_oci
  capi_oci="${capi_version//+/-}"
  capx_oci="${capx_version//+/-}"

  local git_sha="${GITHUB_SHA:-$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo dev)}"
  local short_sha="${git_sha:0:7}"

  DERIVED_REPO_VERSION="$repo_version"
  DERIVED_CAPI_VERSION="$capi_version"
  DERIVED_CAPX_VERSION="$capx_version"
  DERIVED_K8S_VERSION="$k8s_version"
  DERIVED_CAPI_BASE="$capi_base"
  DERIVED_CAPI_OCI="$capi_oci"
  DERIVED_CAPX_OCI="$capx_oci"
  DERIVED_GIT_SHA="$git_sha"
  DERIVED_SHORT_SHA="$short_sha"

  # ── Tag derivation ─────────────────────────────────────────────────────
  # Root images:    repoVersion, repoVersion-capi-<capiOci>, <capiOci>
  # Provider images: repoVersion, repoVersion-capx-<capxOci>-capi-<capiOci>, <capxOci>
  # ForkTag is OCI-sanitized (not raw go list value) so '+' never leaks.
  # No-fork-bump: when CAPI/CAPX unchanged between releases, forkTag stays the
  # same and labels are identical; ko push overwrites the alias to the newest
  # digest.
  local oci_re='^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$'
  DERIVED_ERRORS=()

  # Build maps — declare -ga so callers can read them after derive()
  declare -ga ALL_TAGS=()
  declare -ga TAGS_BY_IMAGE=()

  # validate_tag: enforces tag-schema.md#oci-tag-grammar:
  # OCI regex, no '+', long-tag ≤100 (hard OCI limit 128, reserve headroom).
  validate_tag() {
    local label="$1" value="$2"
    if [[ -z "$value" ]]; then
      DERIVED_ERRORS+=("$label: empty tag (source derivation missing)")
      return
    fi
    if [[ "$value" == *"+"* ]]; then
      DERIVED_ERRORS+=("$label: contains '+' (OCI forbidden) — sanitize with \${ver//+/-}")
    fi
    if ! [[ "$value" =~ $oci_re ]]; then
      DERIVED_ERRORS+=("$label: '$value' fails OCI tag regex $oci_re")
    fi
    # Long-tag limit: 100 chars per tag-schema.md (hard OCI limit is 128,
    # but we reserve headroom for future provider names). Applied to any tag
    # whose label contains 'longTag'; other tags use the OCI 128 bound.
    if [[ "$label" == *"longTag"* ]] && (( ${#value} > 100 )); then
      DERIVED_ERRORS+=("$label: '$value' is ${#value} chars — exceeds 100 long-tag limit")
    elif (( ${#value} > 128 )); then
      DERIVED_ERRORS+=("$label: '$value' is ${#value} chars — exceeds OCI 128 limit")
    fi
  }

  # Define images: name|repoTag|longTag|forkTag
  # Adding a provider is one new row in this table:
  #   Adding a provider is one new row in this table:
  #     "ghcr.io/jimmidyson/kcp-capi-<name>|${repo}|${repo}-<upstream>-<ver>-capi-<capiOci>|<upstreamOci>"
  #   where <upstream> is the short upstream name (capx for Nutanix, capa for
  #   a future AWS provider) and <ver> comes from `go list -m <upstreamImport>`
  #   in that provider's module (see derive() above). For providers that do not
  #   pin a separate fork, reuse the root pattern with capi_oci. No code
  #   restructure is needed — one row, one upstream import, one ko builds entry.
  #   The other three edits are PROVIDER_MODULES + release-please package +
  #   providers/<name>/DRIFT.md.
  local repo="$repo_version"
  local root_long="${repo}-capi-${capi_oci}"
  local capx_long="${repo}-capx-${capx_oci}-capi-${capi_oci}"

  TABLESPEC=(
    "ghcr.io/jimmidyson/kcp-capi-core|${repo}|${root_long}|${capi_oci}"
    "ghcr.io/jimmidyson/kcp-capi-kubeadm-bootstrap|${repo}|${root_long}|${capi_oci}"
    "ghcr.io/jimmidyson/kcp-capi-kubeadm-control-plane|${repo}|${root_long}|${capi_oci}"
    "ghcr.io/jimmidyson/kcp-capi-workspace|${repo}|${root_long}|${capi_oci}"
    "ghcr.io/jimmidyson/kcp-capi-dev-infrastructure|${repo}|${root_long}|${capi_oci}"
    "ghcr.io/jimmidyson/kcp-capi-nutanix-infrastructure|${repo}|${capx_long}|${capx_oci}"
  )
  for row in "${TABLESPEC[@]}"; do
    IFS='|' read -r img r l f <<<"$row"
    validate_tag "$img repoTag" "$r"
    validate_tag "$img longTag" "$l"
    validate_tag "$img forkTag" "$f"
    # The three aliases for one image must be
    # distinct — otherwise a release would collapse two intended tags onto one.
    if [[ "$r" == "$l" ]] || [[ "$r" == "$f" ]] || [[ "$l" == "$f" ]]; then
      DERIVED_ERRORS+=("$img: repo/long/fork not unique — repo=$r long=$l fork=$f")
    fi
    TAGS_BY_IMAGE+=("$img | repo=$r long=$l fork=$f")
    ALL_TAGS+=("$r" "$l" "$f")
  done

  # Also validate branch tags
  validate_tag "branch main" "main"
  validate_tag "sha tag" "sha-${short_sha}"
}

derive

if [[ "$MODE" == "check" ]]; then
  if (( ${#DERIVED_ERRORS[@]} > 0 )); then
    for e in "${DERIVED_ERRORS[@]}"; do echo "ERROR: $e" >&2; done
    exit 1
  fi
  echo "OK: all derived tags pass OCI validation (${#TABLESPEC[@]} images, 3 aliases each + branch/sha)"
  exit 0
fi

if [[ "$MODE" == "capi-version" ]]; then
  echo "$DERIVED_CAPI_VERSION"; exit 0
fi
if [[ "$MODE" == "capx-version" ]]; then
  echo "$DERIVED_CAPX_VERSION"; exit 0
fi

if [[ "$MODE" == "repo-version" ]]; then
  echo "$DERIVED_REPO_VERSION"
  exit 0
fi

if [[ "$MODE" == "gha" ]]; then
  if (( ${#DERIVED_ERRORS[@]} > 0 )); then
    for e in "${DERIVED_ERRORS[@]}"; do echo "ERROR: $e" >&2; done
    exit 1
  fi
  if [[ -z "${GITHUB_OUTPUT:-}" ]]; then
    echo "No GITHUB_OUTPUT set — --gha-output requires it" >&2
    exit 1
  fi
  # Emit per-key values for workflow use (labels + tag-matrix inputs)
  {
    echo "repo_version=${DERIVED_REPO_VERSION}"
    echo "capi_version=${DERIVED_CAPI_VERSION}"
    echo "capx_version=${DERIVED_CAPX_VERSION}"
    echo "k8s_version=${DERIVED_K8S_VERSION}"
    echo "capi_base=${DERIVED_CAPI_BASE}"
    echo "capi_oci=${DERIVED_CAPI_OCI}"
    echo "capx_oci=${DERIVED_CAPX_OCI}"
    echo "git_sha=${DERIVED_GIT_SHA}"
    echo "short_sha=${DERIVED_SHORT_SHA}"
    # Per-image tag CSVs for ko callers that want explicit lists
    for row in "${TABLESPEC[@]}"; do
      IFS='|' read -r img r l f <<<"$row"
      # key like core_tags, nutanix_infrastructure_tags
      key="$(echo "$img" | sed 's|.*/kcp-capi-||; s/-/_/g')_tags"
      echo "${key}=${r},${l},${f}"
    done
  } >> "$GITHUB_OUTPUT"
  exit 0
fi

# Default print mode
if (( ${#DERIVED_ERRORS[@]} > 0 )); then
  for e in "${DERIVED_ERRORS[@]}"; do echo "WARN: $e" >&2; done
fi
cat <<EOF
repo:      $DERIVED_REPO_VERSION
capi:      $DERIVED_CAPI_VERSION (oci: $DERIVED_CAPI_OCI)
capx:      $DERIVED_CAPX_VERSION (oci: $DERIVED_CAPX_OCI)
k8s:       $DERIVED_K8S_VERSION
capi-base: $DERIVED_CAPI_BASE
sha:       $DERIVED_GIT_SHA (short: $DERIVED_SHORT_SHA)

Per-image tags (repo | long | fork):
EOF
for line in "${TAGS_BY_IMAGE[@]}"; do
  echo "  $line"
done
echo "branch:    main, sha-${DERIVED_SHORT_SHA}"
