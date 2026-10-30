#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

KIND_CLUSTER="${KIND_CLUSTER:-kcp-capi-smoke}"
KO_DOCKER_REPO="${KO_DOCKER_REPO:-ko.local}"
IMAGE_TAGS="${IMAGE_TAGS:-local}"

if ! command -v kind >/dev/null 2>&1; then
  echo "kind not found — https://kind.sigs.k8s.io/docs/user/quick-start/#installation" >&2; exit 1
fi

cluster="$KIND_CLUSTER"
repo="$KO_DOCKER_REPO"
tag="$IMAGE_TAGS"

for name in core kubeadm-bootstrap kubeadm-control-plane workspace dev-infrastructure nutanix-infrastructure; do
  img="$repo/kcp-capi-$name:$tag"
  if ! docker image inspect "$img" >/dev/null 2>&1; then
    echo "Local image $img not found — run: task image:build" >&2; exit 1
  fi
  echo "Loading $img into kind cluster $cluster ..."
  kind load docker-image "$img" --name "$cluster"
done
echo "All 6 images loaded into kind/$cluster. Deploy with manifests pointing at $repo/kcp-capi-*:$tag"
