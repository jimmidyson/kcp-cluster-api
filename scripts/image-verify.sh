#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

IMAGE_TAGS="${IMAGE_TAGS:-local}"
KO_DOCKER_REPO="${KO_DOCKER_REPO:-ko.local}"
KIND_CLUSTER="${KIND_CLUSTER:-kcp-capi-smoke}"

scripts/image-tags.sh --check

capi_ver="$(go list -m -json sigs.k8s.io/cluster-api | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
capi_oci="${capi_ver//+/-}"
capx_json="$(go -C providers/nutanix-infrastructure list -m -json github.com/nutanix-cloud-native/cluster-api-provider-nutanix 2>/dev/null || echo '{}')"
capx_ver="$(echo "$capx_json" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('Replace',{}).get('Version','') or d.get('Version',''))" 2>/dev/null || go -C providers/nutanix-infrastructure list -m -json github.com/nutanix-cloud-native/cluster-api-provider-nutanix | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
capx_ver="${capx_ver:-unknown}"
capx_oci="${capx_ver//+/-}"
repo_ver="$(./scripts/image-tags.sh --repo-version 2>/dev/null || git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0-dev)"
k8s_ver="$(go list -m -json k8s.io/api | python3 -c "import json,sys; print(json.load(sys.stdin).get('Version',''))")"
base="$(sed -n 's/^Base: *`//p' DRIFT.md | sed 's/`.*$//' | tr -d '[:space:]')"
root_long="${repo_ver}-capi-${capi_oci}"
capx_long="${repo_ver}-capx-${capx_oci}-capi-${capi_oci}"

echo "Expected: CAPI=$capi_ver ($capi_oci) CAPX=$capx_ver K8S=$k8s_ver base=$base"
echo "Tags: repo=$repo_ver root_long=$root_long capx_long=$capx_long"

tag="$IMAGE_TAGS"
case "$root_long" in *"-capi-$capi_oci") echo "OK: longTag capi segment matches label" ;; *) echo "FAIL: longTag capi segment mismatch" >&2; exit 1 ;; esac
case "$capx_long" in *"-capi-$capi_oci") echo "OK: capx longTag capi segment matches" ;; *) echo "FAIL: capx longTag capi mismatch" >&2; exit 1 ;; esac

tool=""
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then tool=docker
elif command -v buildah >/dev/null 2>&1; then tool=buildah
elif command -v podman >/dev/null 2>&1 && podman info >/dev/null 2>&1; then tool=podman; fi
if [ -z "$tool" ]; then
  echo "No docker/buildah/podman daemon — skipping image label inspection."
  echo "This check runs in GHA (publish-images.yaml) where docker is present."
  echo "SKIPPED: inspect requires docker/buildah/podman"
  exit 0
fi
echo "Using $tool for inspection"
outer_tag="$IMAGE_TAGS"

failures=0
get_label() {
  local image="$1" key="$2"
  case "$tool" in
    docker) docker inspect "$image" 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin)[0]; print(d.get('Config',{}).get('Labels',{}).get('$key',''))" 2>/dev/null || true ;;
    buildah) buildah inspect --type image "$image" 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('Docker',{}).get('config',{}).get('Labels',{}).get('$key',''))" 2>/dev/null || true ;;
    podman) podman inspect "$image" 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin)[0]; print(d.get('Config',{}).get('Labels',{}).get('$key',''))" 2>/dev/null || true ;;
  esac
}
check_image() {
  local imp="$1" exp_capi="$2" exp_long="$3" extra_key="$4" extra_val="$5"
  local ref=""
  set +e
  for cand in "ko.local:$outer_tag" "$KO_DOCKER_REPO:$outer_tag" "$KO_DOCKER_REPO/${imp##*/}:$outer_tag"; do
    $tool image inspect "$cand" >/dev/null 2>&1 && ref="$cand" && break
  done
  set -e
  if [ -z "$ref" ]; then
    echo "  WARN: image for $imp not found (tag $outer_tag) — run image:build first (expected: ko.local/kcp-capi-$imp:$outer_tag)"
    echo "  SKIPPED: $imp not found locally"
    return 0
  fi
  echo "  Checking $imp via $ref..."
  got_capi="$(get_label "$ref" "io.kcp-capi.capi-version")"
  got_k8s="$(get_label "$ref" "io.kcp-capi.k8s-version")"
  got_base="$(get_label "$ref" "io.kcp-capi.capi-base")"
  if [ "$got_capi" != "$exp_capi" ]; then
    echo "    FAIL: io.kcp-capi.capi-version got '$got_capi' expected '$exp_capi'" >&2; failures=$((failures+1))
  else echo "    OK: io.kcp-capi.capi-version=$got_capi"; fi
  if [ -n "$got_k8s" ] && [ "$got_k8s" != "$k8s_ver" ]; then
    echo "    FAIL: io.kcp-capi.k8s-version got '$got_k8s'" >&2; failures=$((failures+1))
  fi
  if [ -n "$got_base" ] && [ "$got_base" != "$base" ]; then
    echo "    FAIL: io.kcp-capi.capi-base got '$got_base' expected '$base'" >&2; failures=$((failures+1))
  fi
  if [ -n "$extra_key" ]; then
    got_extra="$(get_label "$ref" "$extra_key")"
    if [ "$got_extra" != "$extra_val" ]; then
      echo "    FAIL: $extra_key got '$got_extra' expected '$extra_val'" >&2; failures=$((failures+1))
    else echo "    OK: $extra_key=$got_extra"; fi
  fi
}
for imp in core-manager kubeadm-bootstrap-manager kubeadm-control-plane-manager workspace-manager dev-infrastructure-manager; do
  check_image "$imp" "$capi_ver" "$root_long" "" ""
done
check_image "nutanix-infrastructure-manager" "$capi_ver" "$capx_long" "io.kcp-capi.capx-version" "$capx_ver"
if [ "$failures" -gt 0 ]; then echo "$failures mismatch(es)" >&2; exit 1; fi
echo "OK: label/tag verification passed (or skipped where images absent)"
