#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

KO_DOCKER_REPO="${KO_DOCKER_REPO:-ko.local}"
KO_PLATFORMS="${KO_PLATFORMS:-linux/amd64}"
IMAGE_TAGS="${IMAGE_TAGS:-local}"
PROVIDER_MODULES="${PROVIDER_MODULES:-providers/nutanix-infrastructure}"

scripts/image-tags.sh --check

if ! command -v ko >/dev/null 2>&1; then
  echo "ko not found. Install via devbox (devbox shell) or: go install github.com/google/ko@latest" >&2
  exit 1
fi

repo="$KO_DOCKER_REPO"
plat="$KO_PLATFORMS"
tag="$IMAGE_TAGS"
plat_flag=""
if [ -n "$plat" ]; then plat_flag="--platform=$plat"; fi

echo "Building 6 images KO_DOCKER_REPO=$repo IMAGE_TAGS=$tag"

for mod in $PROVIDER_MODULES; do
  name="$(basename "$mod")"
  if ! grep -q "providers/$name/cmd/" .ko.yaml 2>/dev/null; then
    echo "PROVIDER_MODULES member $mod has no .ko.yaml entry for providers/$name/cmd/* — add one" >&2
    exit 1
  fi
done

# Avoid Go template collision in Taskfile: use -json + python.
capi_ver="$(go list -m -json sigs.k8s.io/cluster-api | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
k8s_ver="$(go list -m -json k8s.io/api | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
base="$(sed -n 's/^Base: *`//p' DRIFT.md | sed 's/`.*$//' | tr -d '[:space:]')"
rev="$(git rev-parse HEAD 2>/dev/null || echo dev)"
repo_ver="$(./scripts/image-tags.sh --repo-version 2>/dev/null || git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0-dev)"
common="org.opencontainers.image.source=https://github.com/jimmidyson/kcp-cluster-api,org.opencontainers.image.revision=$rev,org.opencontainers.image.version=$repo_ver,io.kcp-capi.capi-version=$capi_ver,io.kcp-capi.k8s-version=$k8s_ver,io.kcp-capi.capi-base=$base"

# Build root images one by one so each gets its own KO_DOCKER_REPO (with --bare,
# KO_DOCKER_REPO is the full image name; 5 importpaths to one repo would collide).
for imp in ./cmd/core-manager ./cmd/kubeadm-bootstrap-manager ./cmd/kubeadm-control-plane-manager ./cmd/workspace-manager ./cmd/dev-infrastructure-manager; do
  name="$(basename "$imp" | sed 's/-manager$//')"
  KO_DOCKER_REPO="$repo/kcp-capi-$name" ko build --bare $plat_flag --tags "$tag" \
    --image-label "$common" "$imp"
done

capx_json="$(go -C providers/nutanix-infrastructure list -m -json github.com/nutanix-cloud-native/cluster-api-provider-nutanix 2>/dev/null || echo '{}')"
capx_ver="$(echo "$capx_json" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('Replace',{}).get('Version','') or d.get('Version',''))" 2>/dev/null || go -C providers/nutanix-infrastructure list -m -json github.com/nutanix-cloud-native/cluster-api-provider-nutanix | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
KO_DOCKER_REPO="$repo/kcp-capi-nutanix-infrastructure" ko build --bare $plat_flag --tags "$tag" \
  --image-label "$common,io.kcp-capi.capx-version=$capx_ver" \
  ./providers/nutanix-infrastructure/cmd/nutanix-infrastructure-manager

echo "Done. Verify: docker images | grep kcp-capi  (expect 6 ko.local/kcp-capi-*:local)"
