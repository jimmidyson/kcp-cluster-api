#!/usr/bin/env bash
# Provisions the management cluster the stock Cluster API scale test runs on.
#
#   bootstrap    a local kind cluster, with CAPX and CAREN on it
#   clusterclass copy CAREN's ClusterClass and add the etcd backend quota
#   create       create the workload cluster and wait for it
#   kubeconfig   write the workload cluster's kubeconfig
#   install      clusterctl init the scale test's own providers, and prepare them
#   down         delete the workload cluster, then the kind cluster
#
# The kind cluster stays the management cluster for the life of the experiment.
# Nothing is pivoted into the workload cluster: it is the thing being measured,
# and a cluster managing itself would have CAPX and CAREN reconciling on the
# same API server the measurement is reading.
#
# See specs/20260903-140000-upstream-capi-scale/sizing.md for what to ask for
# and why.
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# --- Inputs. Everything here is meant to be overridden from the environment.
BOOTSTRAP_CLUSTER="${BOOTSTRAP_CLUSTER:-capi-scale-bootstrap}"
CLUSTER_NAME="${CLUSTER_NAME:-capi-scale}"
CLUSTER_NAMESPACE="${CLUSTER_NAMESPACE:-default}"
WORKLOAD_KUBECONFIG="${WORKLOAD_KUBECONFIG:-${REPO_ROOT}/bin/${CLUSTER_NAME}.kubeconfig}"

# The Cluster API this test measures. Stock upstream, pinned: a figure without
# the version it was measured on is not a figure.
CAPI_VERSION="${CAPI_VERSION:-v1.14.1}"

# etcd's default 2 GiB backend quota is a cliff a climbing fleet walks off, and
# CAREN has no variable for it — hence the ClusterClass copy below.
ETCD_QUOTA_BYTES="${ETCD_QUOTA_BYTES:-8589934592}"

# The CAREN ClusterClass to copy, and the template to generate the Cluster from.
# Names differ between CAREN versions, so they are inputs rather than
# assumptions; `clusterclass` prints what it found if the name is wrong.
CAREN_CLUSTERCLASS="${CAREN_CLUSTERCLASS:-nutanix-quick-start}"
CAREN_CLUSTERCLASS_NAMESPACE="${CAREN_CLUSTERCLASS_NAMESPACE:-default}"
# CAREN publishes complete clusterctl templates in its releases. The default is
# the Nutanix quick start from the release named below; override CLUSTER_TEMPLATE
# to use your own.
CAREN_VERSION="${CAREN_VERSION:-v0.50.0}"
CLUSTER_TEMPLATE="${CLUSTER_TEMPLATE:-https://raw.githubusercontent.com/nutanix-cloud-native/cluster-api-runtime-extensions-nutanix/${CAREN_VERSION}/examples/capi-quick-start/nutanix-cluster-cilium-helm-addon.yaml}"

# The fleet the sizing document asks for.
CONTROL_PLANE_COUNT="${CONTROL_PLANE_COUNT:-3}"
WORKER_COUNT="${WORKER_COUNT:-4}"

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\nerror: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required and is not on PATH"; }

bootstrap() {
  need kind; need kubectl; need clusterctl; need helm
  if kind get clusters 2>/dev/null | grep -qx "${BOOTSTRAP_CLUSTER}"; then
    log "kind cluster ${BOOTSTRAP_CLUSTER} already exists"
  else
    log "Creating the kind bootstrap cluster ${BOOTSTRAP_CLUSTER}"
    kind create cluster --name "${BOOTSTRAP_CLUSTER}"
  fi
  kubectl config use-context "kind-${BOOTSTRAP_CLUSTER}" >/dev/null

  # CAPX and CAREN go here and only here. They build the cluster; they are not
  # part of what is measured, and installing them on the workload cluster would
  # put two more controller sets on the API server under test.
  #
  # Four things this line needs that are easy to leave out, all of them from
  # CAREN's own documented install:
  #
  #   * CLUSTER_TOPOLOGY=true — the templates are ClusterClass based, and
  #     without the gate the topology controller does not run at all.
  #   * EXP_RUNTIME_SDK=true — CAREN is a runtime extension. Without the gate
  #     its hooks are never called and the cluster comes up unpatched.
  #   * --addon helm — CAREN's templates deploy the CNI and the cloud provider
  #     with strategy: HelmAddon, which is the Helm addon provider's job. Leave
  #     it out and the cluster has no CNI, so no node ever becomes Ready.
  #   * the Nutanix credentials, which clusterctl reads at init time.
  log "Installing Cluster API, CAPX and the Helm addon provider on the bootstrap cluster"
  env CLUSTER_TOPOLOGY=true EXP_RUNTIME_SDK=true \
    clusterctl init \
      --infrastructure "nutanix${CAPX_VERSION:+:${CAPX_VERSION}}" \
      --addon helm \
      --wait-providers

  log "Installing CAREN ${CAREN_VERSION} via Helm"
  helm repo add caren https://nutanix-cloud-native.github.io/cluster-api-runtime-extensions-nutanix/helm
  helm repo update caren
  helm upgrade --install caren caren/cluster-api-runtime-extensions-nutanix \
    --version "${CAREN_VERSION}" \
    --namespace caren-system \
    --create-namespace \
    --wait --wait-for-jobs
}

# clusterclass copies CAREN's ClusterClass under a new name and adds one patch
# to it: etcd's backend quota.
#
# A copy rather than an edit. The CAREN-supplied ClusterClass is managed by
# whatever installed it, so an edit is liable to be reverted underneath a
# running experiment, and a scale run that quietly loses its etcd quota halfway
# up the ladder would look like a cluster that got slower.
clusterclass() {
  need kubectl; need jq
  local src="${CAREN_CLUSTERCLASS}" dst="${CLUSTER_NAME}-scale"
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get clusterclass "${src}" >/dev/null 2>&1 || {
      echo "ClusterClasses available in ${CAREN_CLUSTERCLASS_NAMESPACE}:" >&2
      kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
        get clusterclass -o name >&2 || true
      die "no ClusterClass ${src}: set CAREN_CLUSTERCLASS to one of the above"
    }

  log "Copying ClusterClass ${src} to ${dst} with an etcd backend quota of ${ETCD_QUOTA_BYTES} bytes"
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get clusterclass "${src}" -o json \
    | jq --arg name "${dst}" --arg quota "${ETCD_QUOTA_BYTES}" '
        .metadata = {name: $name, namespace: .metadata.namespace}
        | del(.status)
        | .spec.patches = ((.spec.patches // []) + [{
            name: "etcdBackendQuota",
            description: "Raise etcd quota-backend-bytes. The 2 GiB default is a cliff a climbing fleet walks off, and CAREN has no variable for it.",
            definitions: [{
              selector: {
                apiVersion: .spec.controlPlane.templateRef.apiVersion,
                kind: .spec.controlPlane.templateRef.kind,
                matchResources: {controlPlane: true}
              },
              jsonPatches: [{
                op: "add",
                path: "/spec/template/spec/kubeadmConfigSpec/clusterConfiguration/etcd",
                value: {local: {extraArgs: [{name: "quota-backend-bytes", value: $quota}]}}
              }]
            }]
          }])' \
    | kubectl --context "kind-${BOOTSTRAP_CLUSTER}" apply -f -

  cat <<NOTE

Applied as ClusterClass ${dst}. Two things to check against your CAREN version:

  * extraArgs is a list of {name, value} in the v1beta2 kubeadm API this
    Cluster API uses. If your CAREN ClusterClass is on an older API where
    extraArgs is a map, the patch above needs the map form instead.
  * the patch replaces .etcd wholesale. If your ClusterClass already patches
    etcd, merge the two rather than stacking them.
NOTE
}

create() {
  need kubectl; need clusterctl
  [[ -n "${CLUSTER_TEMPLATE}" ]] || die "set CLUSTER_TEMPLATE to the CAREN cluster template to generate from"

  log "Generating ${CLUSTER_NAME}: ${CONTROL_PLANE_COUNT} control plane, ${WORKER_COUNT} workers"
  # Node labels go through the standard Cluster API path, and they have to be
  # in a domain it will propagate: a Machine label reaches the Node only if it
  # is prefixed node-role.kubernetes.io, or is in the
  # node-restriction.kubernetes.io or node.cluster.x-k8s.io domains. Anything
  # else stops at the Machine, and a node selector against it never matches.
  clusterctl generate cluster "${CLUSTER_NAME}" \
    --kubeconfig-context "kind-${BOOTSTRAP_CLUSTER}" \
    --target-namespace "${CLUSTER_NAMESPACE}" \
    --control-plane-machine-count "${CONTROL_PLANE_COUNT}" \
    --worker-machine-count "${WORKER_COUNT}" \
    --from "${CLUSTER_TEMPLATE}" \
    | go run "${REPO_ROOT}/cmd/capiscale-template" --workers "${WORKER_COUNT}" \
    > "${REPO_ROOT}/bin/${CLUSTER_NAME}.yaml"

  # Two changes the generated manifest needs, both made above:
  #
  #   * a fixed worker count. CAREN's example sets no replicas at all — the
  #     pool is sized by cluster-autoscaler annotations — and a scale test
  #     cannot have its own management cluster resizing underneath it.
  #   * without CSI, COSI, the autoscaler, the service load balancer or node
  #     feature discovery. Nothing here asks for a PersistentVolume, and every
  #     addon left on is another controller reconciling against the API server
  #     whose cost is the subject of the run. The CNI and the cloud provider
  #     stay: without the CNI nothing networks, and without the cloud provider
  #     nodes keep the uninitialized taint and never become schedulable.
  log "Review ${REPO_ROOT}/bin/${CLUSTER_NAME}.yaml, then apply it"
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" apply -f "${REPO_ROOT}/bin/${CLUSTER_NAME}.yaml"

  log "Waiting for the control plane"
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CLUSTER_NAMESPACE}" \
    wait cluster "${CLUSTER_NAME}" --for=condition=ControlPlaneInitialized --timeout=30m
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CLUSTER_NAMESPACE}" \
    wait cluster "${CLUSTER_NAME}" --for=condition=Available --timeout=45m
}

kubeconfig() {
  need clusterctl
  mkdir -p "$(dirname "${WORKLOAD_KUBECONFIG}")"
  clusterctl get kubeconfig "${CLUSTER_NAME}" \
    --kubeconfig-context "kind-${BOOTSTRAP_CLUSTER}" \
    --namespace "${CLUSTER_NAMESPACE}" > "${WORKLOAD_KUBECONFIG}"
  log "Wrote ${WORKLOAD_KUBECONFIG}"
}

# install puts the scale test's own Cluster API on the workload cluster: core,
# both kubeadm providers, and the docker provider that serves DevCluster.
#
# Not CAPX and not CAREN. They built this cluster and have no part in what it
# measures; adding them would put two more controller sets and their CRDs on
# the API server under test.
install() {
  need clusterctl; need kubectl
  [[ -f "${WORKLOAD_KUBECONFIG}" ]] || die "no kubeconfig at ${WORKLOAD_KUBECONFIG}: run '$0 kubeconfig' first"

  log "Installing stock Cluster API ${CAPI_VERSION} on ${CLUSTER_NAME}"
  # The docker provider is what serves DevCluster; the in-memory backend is a
  # mode of it, which is also why its deployment arrives wanting a Docker
  # socket that the prepare step below takes away.
  KUBECONFIG="${WORKLOAD_KUBECONFIG}" clusterctl init \
    --core "cluster-api:${CAPI_VERSION}" \
    --bootstrap "kubeadm:${CAPI_VERSION}" \
    --control-plane "kubeadm:${CAPI_VERSION}" \
    --infrastructure "docker:${CAPI_VERSION}"

  log "Preparing every controller: Guaranteed resources, GOMEMLIMIT, pprof; and no Docker socket"
  go run "${REPO_ROOT}/cmd/capiscale-prepare" --kubeconfig "${WORKLOAD_KUBECONFIG}" "$@"

  log "Waiting for the controllers to come back after the patch"
  for ns in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capd-system; do
    kubectl --kubeconfig "${WORKLOAD_KUBECONFIG}" -n "${ns}" rollout status deploy --timeout=10m
  done
}

down() {
  need kubectl; need kind
  log "Deleting cluster ${CLUSTER_NAME}"
  kubectl --context "kind-${BOOTSTRAP_CLUSTER}" -n "${CLUSTER_NAMESPACE}" \
    delete cluster "${CLUSTER_NAME}" --ignore-not-found --wait --timeout=30m
  log "Deleting the kind bootstrap cluster"
  kind delete cluster --name "${BOOTSTRAP_CLUSTER}"
}

case "${1:-}" in
  bootstrap|clusterclass|create|kubeconfig|install|down) cmd="$1"; shift; "${cmd}" "$@" ;;
  *) sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
