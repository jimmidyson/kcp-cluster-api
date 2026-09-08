#!/usr/bin/env bash
# Provisions the management cluster the stock Cluster API scale test runs on.
#
#   config       resolve and print every input, touching nothing
#   bootstrap    a local kind cluster, with CAPX and CAREN on it
#   clusterclass copy CAREN's ClusterClass; add the etcd quota, its disk, and the control plane tolerances
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

# The pinned tools first. `task tools:capi` puts kind, clusterctl and helm in
# bin/, and clusterctl's version is not incidental: it warns or refuses when it
# is older than the providers it is asked to install, and the Cluster API this
# test installs is the thing being measured. A clusterctl from somewhere else
# is a different tool installing possibly different defaults.
export PATH="${REPO_ROOT}/bin:${PATH}"

# --- Inputs. Everything here is meant to be overridden from the environment.
BOOTSTRAP_CLUSTER="${BOOTSTRAP_CLUSTER:-capi-scale-bootstrap}"
CLUSTER_NAME="${CLUSTER_NAME:-capi-scale}"
CLUSTER_NAMESPACE="${CLUSTER_NAMESPACE:-default}"
WORKLOAD_KUBECONFIG="${WORKLOAD_KUBECONFIG:-${REPO_ROOT}/bin/${CLUSTER_NAME}.kubeconfig}"

# The bootstrap cluster's kubeconfig, written here rather than merged into
# ~/.kube/config.
#
# kind's default is to merge its context into whatever kubeconfig is in play and
# make it current, which leaves someone's shell pointing at a throwaway cluster
# after a step they ran for another reason. This repository already refuses the
# mirror image of that — the scale tasks name a context rather than taking
# whatever is current, so that a run meant for a local cluster cannot create a
# fleet somewhere else — and the same argument runs in this direction.
#
# So the bootstrap cluster lives in a file of its own, every command against it
# names that file, and nothing outside bin/ changes.
BOOTSTRAP_KUBECONFIG="${BOOTSTRAP_KUBECONFIG:-${REPO_ROOT}/bin/${BOOTSTRAP_CLUSTER}.kubeconfig}"

# The Cluster API this test measures, on the cluster under test. Stock upstream,
# pinned: a figure without the version it was measured on is not a figure.
CAPI_VERSION="${CAPI_VERSION:-v1.14.1}"

# The Cluster API on the bootstrap cluster, which is a different question from
# the one above and is pinned for a different reason.
#
# CAREN v0.50.0's runtime extension strict-decodes CAPX's NutanixClusterTemplate
# against the types it was compiled with, and a newer topology controller writes
# a spec.template.metadata that those types do not have. The cluster then never
# gets built:
#
#   failed to generate patches for patch "cluster-config": ... failed to convert
#   unstructured object (infrastructure.cluster.x-k8s.io/v1beta1,
#   Kind=NutanixClusterTemplate) to typed object: strict decoding error: unknown
#   field "spec.template.metadata"
#
# So the bootstrap cluster runs the Cluster API CAREN was built against. This is
# not a constraint on what the test measures — that is CAPI_VERSION, on another
# cluster entirely — and the two differ by default: the cluster under test gets
# the latest release, which is the point of measuring stock Cluster API.
BOOTSTRAP_CAPI_VERSION="${BOOTSTRAP_CAPI_VERSION:-v1.12.5}"

# etcd's default 2 GiB backend quota is a cliff a climbing fleet walks off, and
# CAREN has no variable for it — hence the ClusterClass copy below.
ETCD_QUOTA_BYTES="${ETCD_QUOTA_BYTES:-8589934592}"

# The three tolerances OpenShift ships as production defaults, applied here for
# the reason it applies them: a store that is slow for a minute should cost a
# slow minute, not three process deaths. Measured on this cluster, a five-minute
# compaction on a cold page cache held the etcd apply loop for 59 seconds, and
# kubeadm's defaults turned that into the kubelet killing the API server for
# failing /livez while the controller manager and scheduler lost their leases
# to a 5s GET. Each knob is empty to keep kubeadm's default, and config prints
# which. See the README, "The compaction stall".
#
# The API server's etcd health and ready check timeouts, both, against kubeadm's
# 2s. A check that gives up in two seconds reports a slow store as a dead one.
APISERVER_ETCD_CHECK_TIMEOUT="${APISERVER_ETCD_CHECK_TIMEOUT-9s}"
# Take the etcd check out of the API server's liveness probe, so a slow store
# makes the API server unready — it drops out of the VIP — rather than killed.
# Readiness keeps the check. Anything but "true" keeps kubeadm's /livez.
APISERVER_LIVEZ_EXCLUDE_ETCD="${APISERVER_LIVEZ_EXCLUDE_ETCD-true}"
# Leader election on kube-controller-manager and kube-scheduler, against
# kubeadm's 15s/10s/2s: built to ride out a 78s API server outage. All three or
# none — each validates against the others.
LEADER_ELECT_LEASE_DURATION="${LEADER_ELECT_LEASE_DURATION-137s}"
LEADER_ELECT_RENEW_DEADLINE="${LEADER_ELECT_RENEW_DEADLINE-107s}"
LEADER_ELECT_RETRY_PERIOD="${LEADER_ELECT_RETRY_PERIOD-26s}"
# Two API server knobs, both off by default so a run attributes what it finds
# to one change at a time.
#
# --goaway-chance makes the API server occasionally tell an HTTP/2 client to
# reconnect, so long-lived connections spread across the instances behind a
# load balancer instead of staying wherever they first landed. Measured here:
# the instance behind the VIP served two and a half times the lists and nine
# times the GETs of its peers, and its node's etcd member was the one that
# stalled. Kubernetes documents the flag for exactly this; 0.001 is a common
# setting. Empty leaves it off.
APISERVER_GOAWAY_CHANCE="${APISERVER_GOAWAY_CHANCE-}"
# GOGC on the API server, set through kubeadm's extraEnvs. The collector runs
# when the heap has grown by this percent over what survived the last cycle,
# so 100 lets a 15 GiB live heap reach 30 GiB before collecting. OpenShift
# exposes the same knob and clamps it to 63..100; 63 is its floor. What it
# buys is page cache for the etcd member on the same node, at the cost of API
# server CPU. Empty leaves the Go default of 100.
APISERVER_GOGC="${APISERVER_GOGC-}"
# Protect the etcd member from the API server beside it, at the cgroup.
#
# etcd keeps its backend file mapped and relies on the page cache to hold it;
# the API server's heap is anonymous memory that grows with the fleet; and the
# kernel gives the page cache to whoever is not using anonymous memory. On
# 32 GiB nodes at 1500 clusters that left about 3 GiB of cache for a 2 GB
# file, and a compaction that reads the file cold held the member's apply
# loop for a minute. cgroup v2 memory.min fences a cgroup's memory, file pages
# included, from reclaim, and the kubelet sets it from a pod's memory request
# when its MemoryQoS feature gate is on. The gate is on by default from
# Kubernetes 1.37, so enabling it on 1.36 is asking for next release's
# behaviour a release early. Off by default here so that a run attributes
# what it finds to one change at a time. Needs cgroup v2 on the nodes.
#
# memory.min protects what is requested, and kubeadm requests 100Mi for etcd,
# so the gate is only worth turning on with ETCD_MEMORY_REQUEST sized to hold
# the member: its own heap plus the backend file at the quota it is allowed to
# reach. 6Gi covers a 2 GB file with room to grow; 8Gi is the quota itself.
#
# The gate also sets memory.high on every Burstable container from its request
# and the node allocatable, which on kubeadm's API server, with no memory
# request and no limit, lands at 90% of the node. Above that the kernel
# throttles the API server's allocation rather than letting it take the last
# tenth. Pair with APISERVER_GOGC if that throttling shows up in /livez.
MEMORY_QOS="${MEMORY_QOS-false}"
ETCD_MEMORY_REQUEST="${ETCD_MEMORY_REQUEST-}"

# etcd on a disk of its own. On CAREN's template it shares the root disk with
# the API server's audit log, the container logs and everything else on the
# node that syncs — and under a burst the audit log alone is a record per
# object, tens of thousands of them, on the vdisk the WAL is fsyncing to.
# etcd's own guidance is a dedicated disk and every production control plane
# gives it one. The disk is attached by CAPX, formatted and mounted by
# cloud-init before kubeadm runs, and a node whose disk is not mounted refuses
# to run kubeadm at all rather than quietly putting etcd on the root disk.
# Empty leaves etcd on the root disk, which is what the recorded runs used.
#
# 32Gi is derived, not round: the backend file at its 8 GiB quota, a second
# copy of it while defrag rewrites the file beside the old one, the WAL and
# snapshots under a gigabyte, and room. Below about 17 GiB a defragmentation
# between rungs can fail with the disk full, which would look like the quota.
ETCD_DISK_SIZE="${ETCD_DISK_SIZE-32Gi}"
# The device the guest sees the disk as. The system disk is SCSI index 0 and
# this one is index 1, which Linux names /dev/sdb on AHV; nothing guarantees
# the name, which is why the filesystem is labelled and mounted by label and
# the mount is checked before kubeadm runs.
ETCD_DISK_DEVICE="${ETCD_DISK_DEVICE:-/dev/sdb}"
# Where the disk is mounted — and it is not /var/lib/etcd. mke2fs puts a
# lost+found directory on every new filesystem, and kubeadm's preflight refuses
# an etcd data directory that is not empty, so a disk mounted on /var/lib/etcd
# fails every node with "DirAvailable--var-lib-etcd: /var/lib/etcd is not
# empty". The disk is mounted here and kubeadm is told to use the etcd
# subdirectory of it, which does not exist until kubeadm creates it. The same
# arrangement as Cluster API's Azure provider documents for its etcd disk.
ETCD_DISK_MOUNT="${ETCD_DISK_MOUNT:-/var/lib/etcddisk}"
# The storage container to create it in. Empty leaves the choice to Prism.
ETCD_DISK_STORAGE_CONTAINER="${ETCD_DISK_STORAGE_CONTAINER-${NUTANIX_STORAGE_CONTAINER_NAME:-}}"


# The CAREN ClusterClass to copy, and the template to generate the Cluster from.
# Names differ between CAREN versions, so they are inputs rather than
# assumptions; `clusterclass` prints what it found if the name is wrong.
CAREN_VERSION="${CAREN_VERSION:-v0.50.0}"

# CAPX, unpinned: clusterctl takes its latest unless CAPX_VERSION says
# otherwise. Set it to pin — `CAPX_VERSION=v1.10.3 ./scale-cluster.sh bootstrap`
# — and the run records which version it used either way.
#
# If a CAPX version does turn out not to work here, the thing to check first is
# whether CAREN's ClusterClass still resolves against its types: the chart gates
# that class on infrastructure.cluster.x-k8s.io/v1beta1/NutanixClusterTemplate
# being present, so a CAPX that moves that API produces the empty ClusterClass
# list this run has already seen once.
CAPX_VERSION="${CAPX_VERSION:-}"
CAREN_CLUSTERCLASS="${CAREN_CLUSTERCLASS:-nutanix-quick-start}"
CAREN_CLUSTERCLASS_NAMESPACE="${CAREN_CLUSTERCLASS_NAMESPACE:-default}"

# The copy `clusterclass` makes and `create` points the Cluster at. The copy is
# where etcd's backend quota and metrics port are patched in, so a Cluster
# naming CAREN's own class instead gets neither — invisibly, until something
# needs them.
SCALE_CLUSTERCLASS="${SCALE_CLUSTERCLASS:-${CLUSTER_NAME}-scale}"

# CAREN's default ClusterClasses ship in its Helm chart rather than in the
# runtime-extensions components clusterctl installs, so bootstrap applies this
# one directly. The chart includes the file verbatim (.Files.Get, no
# templating), so applying it is exactly what a Helm install would have done.
CAREN_CLUSTERCLASS_URL="${CAREN_CLUSTERCLASS_URL:-https://raw.githubusercontent.com/nutanix-cloud-native/cluster-api-runtime-extensions-nutanix/${CAREN_VERSION}/charts/cluster-api-runtime-extensions-nutanix/defaultclusterclasses/nutanix-cluster-class.yaml}"

# CAREN publishes complete clusterctl templates in its releases. The default is
# the Nutanix quick start from the release named above; override CLUSTER_TEMPLATE
# to use your own.
CLUSTER_TEMPLATE="${CLUSTER_TEMPLATE:-https://raw.githubusercontent.com/nutanix-cloud-native/cluster-api-runtime-extensions-nutanix/${CAREN_VERSION}/examples/capi-quick-start/nutanix-cluster-cilium-helm-addon.yaml}"

# A login on every node, from NUTANIX_SSH_AUTHORIZED_KEY in the environment.
#
# That variable is CAPX's, from CAPX's own cluster template, and CAREN's quick
# start never reads it — so a key exported for it was silently ignored, and the
# first node that failed cloud-init could not be logged into. CAREN creates
# users through its clusterConfig variable, which is where the trimmer writes
# the key. Unset leaves the cluster with no login, as the recorded runs had.
SSH_USER="${SSH_USER:-capiuser}"

# The fleet the sizing document asks for.
CONTROL_PLANE_COUNT="${CONTROL_PLANE_COUNT:-3}"
WORKER_COUNT="${WORKER_COUNT:-4}"

# Node sizes. CAREN's example builds every node at 2 vCPU and 4 GiB, which is a
# sensible quick start and a sixth of what sizing.md asks the control plane for.
# Nothing in a run reports that as wrong — the cluster comes up, the controllers
# schedule, and the ceiling the ladder finds is the box rather than Cluster API.
# Keep the CSI addon. The stock run asks for no PersistentVolume and the trimmer
# removes it; the kcp side of the comparison gives its etcd a volume per member,
# so a cluster that will host both needs a provisioner. Off by default, because
# the recorded stock figures were taken without it.
KEEP_CSI="${KEEP_CSI:-false}"
# Split the worker pool so the kcp side's shard and store get nodes of their
# own, labelled by the topology rather than by hand. Carved out of WORKER_COUNT,
# not added to it. Zero leaves the single pool the recorded stock runs used.
CONTROL_PLANE_POOL_WORKERS="${CONTROL_PLANE_POOL_WORKERS:-0}"

CONTROL_PLANE_VCPUS="${CONTROL_PLANE_VCPUS:-16}"
CONTROL_PLANE_MEMORY="${CONTROL_PLANE_MEMORY:-64Gi}"
CONTROL_PLANE_DISK="${CONTROL_PLANE_DISK:-200Gi}"
WORKER_VCPUS="${WORKER_VCPUS:-16}"
WORKER_MEMORY="${WORKER_MEMORY:-32Gi}"
WORKER_DISK="${WORKER_DISK:-100Gi}"

# config resolves and prints every input.
#
# It exists because the block above is ordered: one variable's default is built
# from another's, and `set -u` turns a reference to a not-yet-assigned name into
# an error three steps into a provisioning run. Running this touches nothing and
# fails immediately if that ordering is wrong again.
config() {
  # Worked out before the heredoc rather than inside it: a ${var:-default}
  # carrying punctuation is parsed by bash, not printed, and this one broke on
  # its own apostrophe.
  local capx="${CAPX_VERSION}"
  [[ -n "${capx}" ]] || capx="unpinned, clusterctl takes its latest (set CAPX_VERSION to pin)"
  local etcdcheck="${APISERVER_ETCD_CHECK_TIMEOUT}"
  [[ -n "${etcdcheck}" ]] || etcdcheck="kubeadm default, 2s (APISERVER_ETCD_CHECK_TIMEOUT is empty)"
  local livez="/livez?exclude=etcd (a slow store makes the API server unready, not dead)"
  [[ "${APISERVER_LIVEZ_EXCLUDE_ETCD}" == "true" ]] || livez="kubeadm default, /livez including the etcd check"
  local leader="lease ${LEADER_ELECT_LEASE_DURATION}, renew ${LEADER_ELECT_RENEW_DEADLINE}, retry ${LEADER_ELECT_RETRY_PERIOD} (OpenShift; kubeadm is 15s/10s/2s)"
  [[ -n "${LEADER_ELECT_LEASE_DURATION}" ]] || leader="kubeadm default, 15s/10s/2s (LEADER_ELECT_LEASE_DURATION is empty)"
  local goaway="off (APISERVER_GOAWAY_CHANCE is empty; connections stay on the instance they first reached)"
  [[ -z "${APISERVER_GOAWAY_CHANCE}" ]] || goaway="${APISERVER_GOAWAY_CHANCE} (HTTP/2 clients are occasionally told to reconnect, spreading them across instances)"
  local gogc="Go default, 100 (APISERVER_GOGC is empty)"
  [[ -z "${APISERVER_GOGC}" ]] || gogc="${APISERVER_GOGC} (OpenShift clamps this to 63..100)"
  local memoryqos="off (MEMORY_QOS is not true; the page cache is shared and unprotected)"
  [[ "${MEMORY_QOS}" != "true" ]] || memoryqos="on: the kubelet sets cgroup v2 memory.min from each pod memory request on the control plane nodes"
  local etcdrequest="kubeadm default, 100Mi (ETCD_MEMORY_REQUEST is empty)"
  [[ -z "${ETCD_MEMORY_REQUEST}" ]] || etcdrequest="${ETCD_MEMORY_REQUEST}$([[ "${MEMORY_QOS}" == "true" ]] && echo ', fenced from reclaim as memory.min' || echo ' (scheduler and OOM ordering only; set MEMORY_QOS=true to fence it from reclaim)')"
  local sshlogin="none (NUTANIX_SSH_AUTHORIZED_KEY is unset; CAREN's template does not read it, this script does)"
  [[ -z "${NUTANIX_SSH_AUTHORIZED_KEY:-}" ]] || sshlogin="user ${SSH_USER}, key ${NUTANIX_SSH_AUTHORIZED_KEY##* } (from NUTANIX_SSH_AUTHORIZED_KEY, via CAREN's users variable)"
  local etcddisk="kubeadm default, /var/lib/etcd on the root disk (ETCD_DISK_SIZE is empty)"
  if [[ -n "${ETCD_DISK_SIZE}" ]]; then
    etcddisk="${ETCD_DISK_SIZE} as ${ETCD_DISK_DEVICE}, mounted on ${ETCD_DISK_MOUNT}, etcd data in ${ETCD_DISK_MOUNT}/etcd"
    [[ -z "${ETCD_DISK_STORAGE_CONTAINER}" ]] || etcddisk="${etcddisk}, in storage container ${ETCD_DISK_STORAGE_CONTAINER}"
  fi
  cat <<CONFIG
bootstrap cluster        ${BOOTSTRAP_CLUSTER}
  kubeconfig             ${BOOTSTRAP_KUBECONFIG}
cluster                  ${CLUSTER_NAME} (namespace ${CLUSTER_NAMESPACE})
  control plane nodes    ${CONTROL_PLANE_COUNT} x ${CONTROL_PLANE_VCPUS} vCPU / ${CONTROL_PLANE_MEMORY} / ${CONTROL_PLANE_DISK} disk
  worker nodes           ${WORKER_COUNT} x ${WORKER_VCPUS} vCPU / ${WORKER_MEMORY} / ${WORKER_DISK} disk
  kubeconfig             ${WORKLOAD_KUBECONFIG}
CAPX                     ${capx}
CAREN                    ${CAREN_VERSION}
  ClusterClass           ${CAREN_CLUSTERCLASS} in ${CAREN_CLUSTERCLASS_NAMESPACE}
  patched copy           ${SCALE_CLUSTERCLASS} (what the Cluster names)
  ClusterClass from      ${CAREN_CLUSTERCLASS_URL}
  cluster template       ${CLUSTER_TEMPLATE}
Cluster API on bootstrap ${BOOTSTRAP_CAPI_VERSION}
Cluster API under test   ${CAPI_VERSION}
etcd backend quota       ${ETCD_QUOTA_BYTES} bytes
API server etcd checks   ${etcdcheck}
API server liveness      ${livez}
leader election          ${leader} — on kube-controller-manager and kube-scheduler
API server goaway-chance ${goaway}
API server GOGC          ${gogc}
memory QoS               ${memoryqos}
etcd memory request      ${etcdrequest}
etcd disk                ${etcddisk}
node login               ${sshlogin}
CSI addon kept           ${KEEP_CSI} (needed only by the kcp side's etcd volumes)
  control plane pool       ${CONTROL_PLANE_POOL_WORKERS} of ${WORKER_COUNT} workers (0 = one unlabelled pool)
CONFIG
}

# clusterctl_for prints the path to a clusterctl of the given version, fetching
# it into bin/ if it is not already there.
#
# One per version, because the two clusters run different Cluster APIs and
# clusterctl checks the contract version of what it is asked to install against
# the one it was built for. Whether a given pair is accepted is a question with
# a real answer and no reason to depend on it: a tool that matches what it
# installs cannot be the thing that fails.
clusterctl_for() {
  local version="$1" path="${REPO_ROOT}/bin/clusterctl-${1}"
  if [[ ! -x "${path}" ]]; then
    local os arch
    os="$(go env GOOS)"
    arch="$(go env GOARCH)"
    mkdir -p "${REPO_ROOT}/bin"
    curl -sSfLo "${path}" \
      "https://github.com/kubernetes-sigs/cluster-api/releases/download/${version}/clusterctl-${os}-${arch}" >&2
    chmod +x "${path}"
  fi
  printf '%s' "${path}"
}

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\nerror: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required and is not on PATH"; }

bootstrap() {
  need kind; need kubectl; need curl; need go
  if kind get clusters 2>/dev/null | grep -qx "${BOOTSTRAP_CLUSTER}"; then
    log "kind cluster ${BOOTSTRAP_CLUSTER} already exists"
  else
    log "Creating the kind bootstrap cluster ${BOOTSTRAP_CLUSTER}"
    kind create cluster --name "${BOOTSTRAP_CLUSTER}" --kubeconfig "${BOOTSTRAP_KUBECONFIG}"
  fi
  # An existing cluster may predate this file, or have been made by hand.
  kind export kubeconfig --name "${BOOTSTRAP_CLUSTER}" --kubeconfig "${BOOTSTRAP_KUBECONFIG}"

  # CAREN is a clusterctl provider, given somewhere to find it. Written here
  # rather than into the operator's own ~/.config/cluster-api/clusterctl.yaml:
  # a scale run should not edit a file the rest of someone's work depends on,
  # and a config it wrote itself is one the next run can rely on.
  mkdir -p "${REPO_ROOT}/bin"
  local config="${REPO_ROOT}/bin/clusterctl-${BOOTSTRAP_CLUSTER}.yaml"
  cat > "${config}" <<YAML
providers:
  - name: "caren"
    url: "https://github.com/nutanix-cloud-native/cluster-api-runtime-extensions-nutanix/releases/${CAREN_VERSION}/runtime-extensions-components.yaml"
    type: "RuntimeExtensionProvider"
YAML

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
  #     it out and the cluster has no CNI, so no node ever becomes Ready. This
  #     is the provider, not the helm CLI, which nothing here needs.
  #   * the Nutanix credentials, which clusterctl reads at init time.
  log "Installing Cluster API, CAPX, the Helm addon provider and CAREN ${CAREN_VERSION}"
  env CLUSTER_TOPOLOGY=true EXP_RUNTIME_SDK=true \
    "$(clusterctl_for "${BOOTSTRAP_CAPI_VERSION}")" init \
      --kubeconfig "${BOOTSTRAP_KUBECONFIG}" \
      --config "${config}" \
      --core "cluster-api:${BOOTSTRAP_CAPI_VERSION}" \
      --bootstrap "kubeadm:${BOOTSTRAP_CAPI_VERSION}" \
      --control-plane "kubeadm:${BOOTSTRAP_CAPI_VERSION}" \
      --infrastructure "nutanix${CAPX_VERSION:+:${CAPX_VERSION}}" \
      --addon helm \
      --runtime-extension "caren:${CAREN_VERSION}" \
      --wait-providers

  # The ClusterClass, which clusterctl does not install.
  #
  # CAREN's providers artifact carries the runtime extension and nothing else;
  # its default ClusterClasses live in the Helm chart, gated on
  # .Values.deployDefaultClusterClasses and on CAPX being present. Installing
  # CAREN through clusterctl therefore leaves a cluster with the extension
  # running and no class for a Cluster to name — which presents as an empty
  # ClusterClass list and nothing to say why.
  #
  # Applied after clusterctl init because the class refers to CAPX's types, and
  # applied from the chart's own file because the chart includes it verbatim.
  log "Applying CAREN's default Nutanix ClusterClass, which its clusterctl components do not carry"
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    apply -f "${CAREN_CLUSTERCLASS_URL}"
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
  local src="${CAREN_CLUSTERCLASS}" dst="${SCALE_CLUSTERCLASS}"
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get clusterclass "${src}" >/dev/null 2>&1 || {
      echo "ClusterClasses available in ${CAREN_CLUSTERCLASS_NAMESPACE}:" >&2
      kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
        get clusterclass -o name >&2 || true
      cat >&2 <<NOTE

no ClusterClass ${src} in ${CAREN_CLUSTERCLASS_NAMESPACE}.

If that list was empty, nothing installed one. CAREN's default ClusterClasses
ship in its Helm chart, not in the runtime-extensions components clusterctl
installs, so a clusterctl-only install leaves the extension running and no class
for a Cluster to name. Re-run '$0 bootstrap', which applies it, or apply it by
hand:

  kubectl --kubeconfig ${BOOTSTRAP_KUBECONFIG} apply -f ${CAREN_CLUSTERCLASS_URL}

If the list had entries, set CAREN_CLUSTERCLASS to one of them.
NOTE
      exit 1
    }

  # Two changes, both for the same reason: this run expects the store to be
  # what runs out, and needs to be able to see it. The quota is the cliff, and
  # the metrics port is how anything outside the node knows how close it is.
  #
  # The metrics port carries no data and no authentication — it is etcd's
  # /metrics, not its client API — which is a fair trade on a throwaway scale
  # cluster and would not be on anything else.
  #
  # And the three OpenShift tolerances on the control plane, made differently
  # from the etcd patch: every argument is *appended* to the list the template
  # already carries rather than the list being replaced. The README records
  # three failed attempts to change an argument CAREN already sets; these are
  # not that. The names are new, so the uniqueness rule that refused the first
  # attempt does not apply; "-" is the one array index the patch validator
  # allows on add, so the second attempt's refusal does not either; and this
  # patch is last in spec.patches, so it renders after CAREN's runtime
  # extension and lands on the lists that extension produced rather than
  # replacing them, which is what broke a control plane on the third attempt.
  #
  # The liveness probe is not an argument. kubeadm generates the probes and has
  # no knob for them, but it applies patch files to the static pod manifests it
  # writes, from the directory initConfiguration.patches names — and CAREN's
  # class already names one and writes its kubelet patches there. So the probe
  # is one more file appended to kubeadmConfigSpec.files, and no post-kubeadm
  # command is needed.
  if [[ -n "${LEADER_ELECT_LEASE_DURATION}${LEADER_ELECT_RENEW_DEADLINE}${LEADER_ELECT_RETRY_PERIOD}" ]] \
     && [[ -z "${LEADER_ELECT_LEASE_DURATION}" || -z "${LEADER_ELECT_RENEW_DEADLINE}" || -z "${LEADER_ELECT_RETRY_PERIOD}" ]]; then
    die "set all three of LEADER_ELECT_LEASE_DURATION, LEADER_ELECT_RENEW_DEADLINE and LEADER_ELECT_RETRY_PERIOD, or none: each validates against the others"
  fi

  local class_json kcpt patch_dir set_patch_dir=false
  class_json="$(kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get clusterclass "${src}" -o json)"
  kcpt="$(jq -r '.spec.controlPlane.templateRef.name' <<<"${class_json}")"

  # The patch directory is read from the control plane template rather than
  # assumed. Naming a different one would silently drop every patch CAREN
  # writes to its own, which is how a kubelet comes up without the hardening
  # it was given. Only a template that names none gets the default, and then
  # both init and join are told, since control plane nodes after the first
  # join.
  patch_dir="$(kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get kubeadmcontrolplanetemplates.controlplane.cluster.x-k8s.io "${kcpt}" \
    -o jsonpath='{.spec.template.spec.kubeadmConfigSpec.initConfiguration.patches.directory}')"
  if [[ -z "${patch_dir}" ]]; then
    patch_dir=/etc/kubernetes/patches
    set_patch_dir=true
  fi

  # Whether the template already carries an extraEnvs list on the API server
  # decides whether GOGC appends to it or creates it: a JSON patch cannot add
  # to a list that is not there, and creating one over a list that is would
  # drop what CAREN put in it.
  local has_api_envs=false
  if [[ -n "$(kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CAREN_CLUSTERCLASS_NAMESPACE}" \
    get kubeadmcontrolplanetemplates.controlplane.cluster.x-k8s.io "${kcpt}" \
    -o jsonpath='{.spec.template.spec.kubeadmConfigSpec.clusterConfiguration.apiServer.extraEnvs}')" ]]; then
    has_api_envs=true
  fi

  # A strategic merge patch against the Pod kubeadm generates. Containers merge
  # by name, so only the probe path changes; host, port and scheme stay as
  # kubeadm wrote them. The file name is what kubeadm matches on: target,
  # an alphanumeric suffix, and the patch type.
  local probe
  probe="$(cat <<'PATCH'
spec:
  containers:
    - name: kube-apiserver
      livenessProbe:
        httpGet:
          path: /livez?exclude=etcd
PATCH
)"

  # Two more patch files for the same directory. The kubelet one carries a
  # feature gate, since kubelet gates live in KubeletConfiguration rather than
  # on kubeadm; the suffix sits between the two CAREN writes at 0 and 1 and
  # the one its extension writes at 99, and kubeadm applies them in name
  # order. The etcd one is a memory request on the static pod, which is what
  # memory.min is set from.
  local memoryqos_patch etcdrequest_patch
  memoryqos_patch="$(cat <<'PATCH'
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  MemoryQoS: true
PATCH
)"
  etcdrequest_patch="$(cat <<PATCH
spec:
  containers:
    - name: etcd
      resources:
        requests:
          memory: ${ETCD_MEMORY_REQUEST}
PATCH
)"

  log "Copying ClusterClass ${src} to ${dst}: etcd quota ${ETCD_QUOTA_BYTES} bytes, metrics on :2381"
  log "  API server etcd checks ${APISERVER_ETCD_CHECK_TIMEOUT:-kubeadm default}, liveness $([[ "${APISERVER_LIVEZ_EXCLUDE_ETCD}" == "true" ]] && echo '/livez?exclude=etcd' || echo 'kubeadm default'), leader election ${LEADER_ELECT_LEASE_DURATION:-kubeadm default}/${LEADER_ELECT_RENEW_DEADLINE:-}/${LEADER_ELECT_RETRY_PERIOD:-} (patches in ${patch_dir})"
  log "  API server goaway-chance ${APISERVER_GOAWAY_CHANCE:-off}, GOGC ${APISERVER_GOGC:-Go default}"
  log "  memory QoS ${MEMORY_QOS}, etcd memory request ${ETCD_MEMORY_REQUEST:-kubeadm default}"
  jq --arg name "${dst}" --arg quota "${ETCD_QUOTA_BYTES}" \
     --arg etcdcheck "${APISERVER_ETCD_CHECK_TIMEOUT}" \
     --arg livez "${APISERVER_LIVEZ_EXCLUDE_ETCD}" \
     --arg lease "${LEADER_ELECT_LEASE_DURATION}" \
     --arg renew "${LEADER_ELECT_RENEW_DEADLINE}" \
     --arg retry "${LEADER_ELECT_RETRY_PERIOD}" \
     --arg patchdir "${patch_dir}" --argjson setpatchdir "${set_patch_dir}" \
     --arg probe "${probe}" \
     --arg goaway "${APISERVER_GOAWAY_CHANCE}" --arg gogc "${APISERVER_GOGC}" --argjson hasapienvs "${has_api_envs}" \
     --arg memoryqos "${MEMORY_QOS}" --arg memoryqospatch "${memoryqos_patch}" \
     --arg etcdrequest "${ETCD_MEMORY_REQUEST}" --arg etcdrequestpatch "${etcdrequest_patch}" \
     --arg disksize "${ETCD_DISK_SIZE}" --arg diskdev "${ETCD_DISK_DEVICE}" --arg diskmount "${ETCD_DISK_MOUNT}" \
     --arg diskcontainer "${ETCD_DISK_STORAGE_CONTAINER}" '
        .metadata = {name: $name, namespace: .metadata.namespace}
        | del(.status)
        | (.spec.controlPlane.templateRef) as $cp
        | (.spec.controlPlane.machineInfrastructure.templateRef) as $cpinfra
        | "/spec/template/spec/kubeadmConfigSpec" as $k
        | (
            (if $etcdcheck == "" then [] else [
              {op: "add", path: "\($k)/clusterConfiguration/apiServer/extraArgs/-",
               value: {name: "etcd-healthcheck-timeout", value: $etcdcheck}},
              {op: "add", path: "\($k)/clusterConfiguration/apiServer/extraArgs/-",
               value: {name: "etcd-readycheck-timeout", value: $etcdcheck}}
            ] end)
            + (if $lease == "" then [] else
                (["controllerManager", "scheduler"] | map(
                  {op: "add", path: "\($k)/clusterConfiguration/\(.)/extraArgs/-",
                   value: {name: "leader-elect-lease-duration", value: $lease}},
                  {op: "add", path: "\($k)/clusterConfiguration/\(.)/extraArgs/-",
                   value: {name: "leader-elect-renew-deadline", value: $renew}},
                  {op: "add", path: "\($k)/clusterConfiguration/\(.)/extraArgs/-",
                   value: {name: "leader-elect-retry-period", value: $retry}}
                )) end)
            + (
                (if $livez != "true" then [] else [
                  {path: "\($patchdir)/kube-apiserver1+strategic.yaml", permissions: "0600", content: $probe}
                ] end)
                + (if $memoryqos != "true" then [] else [
                  {path: "\($patchdir)/kubeletconfiguration50+strategic.yaml", permissions: "0600", content: $memoryqospatch}
                ] end)
                + (if $etcdrequest == "" then [] else [
                  {path: "\($patchdir)/etcd1+strategic.yaml", permissions: "0600", content: $etcdrequestpatch}
                ] end)
              ) as $patchfiles
            | ($patchfiles | map({op: "add", path: "\($k)/files/-", value: .}))
            + (if ($patchfiles | length) > 0 and $setpatchdir then [
                {op: "add", path: "\($k)/initConfiguration/patches", value: {directory: $patchdir}},
                {op: "add", path: "\($k)/joinConfiguration/patches", value: {directory: $patchdir}}
              ] else [] end)
            + (if $goaway == "" then [] else [
                {op: "add", path: "\($k)/clusterConfiguration/apiServer/extraArgs/-",
                 value: {name: "goaway-chance", value: $goaway}}
              ] end)
            + (if $gogc == "" then [] else
                (if $hasapienvs then [
                  {op: "add", path: "\($k)/clusterConfiguration/apiServer/extraEnvs/-",
                   value: {name: "GOGC", value: $gogc}}
                ] else [
                  {op: "add", path: "\($k)/clusterConfiguration/apiServer/extraEnvs",
                   value: [{name: "GOGC", value: $gogc}]}
                ] end)
              end)
          ) as $tolerances
        | .spec.patches = ((.spec.patches // []) + [{
            name: "etcdBackendQuota",
            description: "Raise etcd quota-backend-bytes, and open its metrics port. The 2 GiB default is a cliff a climbing fleet walks off, and kubeadm points --listen-metrics-urls at 127.0.0.1, which no scraper outside the node can reach. CAREN has a variable for neither.",
            definitions: [{
              selector: {
                apiVersion: $cp.apiVersion,
                kind: $cp.kind,
                matchResources: {controlPlane: true}
              },
              jsonPatches: [{
                op: "add",
                path: "\($k)/clusterConfiguration/etcd",
                value: {local: ({extraArgs: [
                  {name: "quota-backend-bytes", value: $quota},
                  {name: "listen-metrics-urls", value: "http://0.0.0.0:2381"}
                ]} + (if $disksize == "" then {} else {dataDir: "\($diskmount)/etcd"} end))}
              }]
            }]
          }]
          + (if ($tolerances | length) == 0 then [] else [{
            name: "controlPlaneTolerances",
            description: "The OpenShift production defaults that let a control plane ride out a slow minute from its store: 9s etcd health and ready checks and a liveness probe that excludes etcd on the API server, and 137s/107s/26s leader election on the controller manager and scheduler. Every argument is appended under a new name, last in the patch order, so it lands on the lists the CAREN runtime extension produced. The probe is a kubeadm patch file, since kubeadm has no knob for probes. When set, goaway-chance spreads HTTP/2 clients across the API server instances, GOGC bounds the API server heap so the etcd member beside it keeps its page cache, and the MemoryQoS kubelet gate with an etcd memory request fences that page cache at the cgroup.",
            definitions: [{
              selector: {
                apiVersion: $cp.apiVersion,
                kind: $cp.kind,
                matchResources: {controlPlane: true}
              },
              jsonPatches: $tolerances
            }]
          }] end)
          + (if $disksize == "" then [] else [{
            name: "etcdDisk",
            description: "etcd on a disk of its own. The template puts /var/lib/etcd on the root disk beside the API server audit log and the container logs, so under a burst the WAL fsyncs behind tens of thousands of audit records on one vdisk. CAPX attaches the disk, cloud-init partitions, formats and mounts it by label before kubeadm runs, kubeadm is pointed at the etcd subdirectory of the mount (the mount itself carries lost+found, which the preflight refuses), and a node whose disk is not mounted refuses to run kubeadm rather than putting etcd on the root disk quietly.",
            definitions: [{
              selector: {
                apiVersion: $cpinfra.apiVersion,
                kind: $cpinfra.kind,
                matchResources: {controlPlane: true}
              },
              jsonPatches: [{
                op: "add",
                path: "/spec/template/spec/dataDisks",
                value: [({
                  diskSize: $disksize,
                  deviceProperties: {deviceType: "Disk", adapterType: "SCSI", deviceIndex: 1}
                } + (if $diskcontainer == "" then {} else {
                  storageConfig: {diskMode: "Standard", storageContainer: {type: "name", name: $diskcontainer}}
                } end))]
              }]
            }, {
              selector: {
                apiVersion: $cp.apiVersion,
                kind: $cp.kind,
                matchResources: {controlPlane: true}
              },
              jsonPatches: [{
                op: "add",
                path: "\($k)/diskSetup",
                value: {
                  partitions: [{device: $diskdev, layout: true, overwrite: false, tableType: "gpt"}],
                  filesystems: [{device: $diskdev, filesystem: "ext4", label: "etcd",
                                 extraOpts: ["-F", "-E", "lazy_itable_init=1,lazy_journal_init=1"]}]
                }
              }, {
                op: "add",
                path: "\($k)/mounts",
                value: [["LABEL=etcd", $diskmount, "ext4", "defaults,noatime,nofail"]]
              }, {
                op: "add",
                path: "\($k)/preKubeadmCommands/-",
                value: "mountpoint -q \($diskmount) || { echo \"etcd disk is not mounted on \($diskmount); refusing to run kubeadm with etcd on the root disk\" >&2; exit 1; }"
              }]
            }]
          }] end))' <<<"${class_json}" \
    | kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" apply -f -

  cat <<NOTE

Applied as ClusterClass ${dst}. Things to check against your CAREN version:

  * extraArgs is a list of {name, value} in the v1beta2 kubeadm API this
    Cluster API uses. If your CAREN ClusterClass is on an older API where
    extraArgs is a map, the patches above need the map form instead.
  * the etcd patch replaces .etcd wholesale. If your ClusterClass already
    patches etcd, merge the two rather than stacking them.
  * the tolerances append to apiServer, controllerManager and scheduler
    extraArgs, which must already exist on the template (CAREN's carry
    profiling in all three). If your ClusterClass already sets any of these
    names, the KubeadmControlPlane is refused at admission with "extraArgs name
    must be unique", and the fix is to empty that knob.
  * GOGC goes through apiServer.extraEnvs, which needs Cluster API v1.8 or
    later on the bootstrap cluster and kubeadm from Kubernetes 1.28 or later
    on the nodes; older kubeadm ignores the field and the API server keeps
    the Go default. The patch $([[ "${has_api_envs}" == true ]] && echo "appends to the extraEnvs the template already carries" || echo "creates the extraEnvs list, since the template has none").
  * MEMORY_QOS writes a KubeletConfiguration patch turning the MemoryQoS
    feature gate on for the control plane nodes only, which needs cgroup v2
    there. It is on by default from Kubernetes 1.37. The kubelet then sets
    memory.min from every pod memory request, so ETCD_MEMORY_REQUEST is what
    etcd is actually fenced with, and memory.high on Burstable containers
    from request and node allocatable, which puts the API server at 90% of
    the node.
  * the probe patch is written to ${patch_dir}, read from the control plane
    template$([[ "${set_patch_dir}" == true ]] && echo " — the template named none, so both init and join are told" || echo "")".
  * the etcd disk is a CAPX dataDisks entry on the control plane machine
    template (CAPX v1.5 or later), and diskSetup, mounts and a
    preKubeadmCommands guard on the kubeadm config. If the template already
    carries any of those three, the add replaces it — merge by hand instead.
    After the roll, "findmnt ${ETCD_DISK_MOUNT}" on a control plane node must
    show the data disk and etcd's --data-dir must be ${ETCD_DISK_MOUNT}/etcd; a
    node that fails the guard never runs kubeadm and its Machine stays unready,
    which is the failure to look for.

Nothing else on the API server is patched. See the README: changing an argument
CAREN already sets, profiling among them, cannot be done from a ClusterClass
patch on a CAREN class, and trying broke a control plane rather than failing
cleanly. Appending a new one can.

On a cluster that already exists this rolls the control plane, one machine at a
time — see the README, "Changing the ClusterClass on a cluster that already
exists". The API servers come back fresh, which is the baseline a measured run
wants anyway.
NOTE
}

create() {
  need kubectl; need curl; need go
  [[ -n "${CLUSTER_TEMPLATE}" ]] || die "set CLUSTER_TEMPLATE to the CAREN cluster template to generate from"

  log "Generating ${CLUSTER_NAME}: ${CONTROL_PLANE_COUNT} control plane, ${WORKER_COUNT} workers"
  # Node labels go through the standard Cluster API path, and they have to be
  # in a domain it will propagate: a Machine label reaches the Node only if it
  # is prefixed node-role.kubernetes.io, or is in the
  # node-restriction.kubernetes.io or node.cluster.x-k8s.io domains. Anything
  # else stops at the Machine, and a node selector against it never matches.
  "$(clusterctl_for "${BOOTSTRAP_CAPI_VERSION}")" generate cluster "${CLUSTER_NAME}" \
    --kubeconfig "${BOOTSTRAP_KUBECONFIG}" \
    --target-namespace "${CLUSTER_NAMESPACE}" \
    --control-plane-machine-count "${CONTROL_PLANE_COUNT}" \
    --worker-machine-count "${WORKER_COUNT}" \
    --from "${CLUSTER_TEMPLATE}" \
    | go run "${REPO_ROOT}/cmd/capiscale-template" \
        --workers "${WORKER_COUNT}" \
        --keep-csi="${KEEP_CSI}" \
        --control-plane-pool-workers="${CONTROL_PLANE_POOL_WORKERS}" \
        --control-plane-vcpus "${CONTROL_PLANE_VCPUS}" \
        --control-plane-memory "${CONTROL_PLANE_MEMORY}" \
        --control-plane-disk "${CONTROL_PLANE_DISK}" \
        --worker-vcpus "${WORKER_VCPUS}" \
        --worker-memory "${WORKER_MEMORY}" \
        --worker-disk "${WORKER_DISK}" \
        --cluster-class "${SCALE_CLUSTERCLASS}" \
        --ssh-user "${SSH_USER}" \
        --ssh-authorized-key "${NUTANIX_SSH_AUTHORIZED_KEY:-}" \
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
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" apply -f "${REPO_ROOT}/bin/${CLUSTER_NAME}.yaml"

  log "Waiting for the control plane"
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CLUSTER_NAMESPACE}" \
    wait cluster "${CLUSTER_NAME}" --for=condition=ControlPlaneInitialized --timeout=30m
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CLUSTER_NAMESPACE}" \
    wait cluster "${CLUSTER_NAME}" --for=condition=Available --timeout=45m
}

kubeconfig() {
  need curl; need go
  mkdir -p "$(dirname "${WORKLOAD_KUBECONFIG}")"
  "$(clusterctl_for "${BOOTSTRAP_CAPI_VERSION}")" get kubeconfig "${CLUSTER_NAME}" \
    --kubeconfig "${BOOTSTRAP_KUBECONFIG}" \
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
  need kubectl; need curl; need go
  [[ -f "${WORKLOAD_KUBECONFIG}" ]] || die "no kubeconfig at ${WORKLOAD_KUBECONFIG}: run '$0 kubeconfig' first"

  log "Installing stock Cluster API ${CAPI_VERSION} on ${CLUSTER_NAME}"
  # The docker provider is what serves DevCluster; the in-memory backend is a
  # mode of it, which is also why its deployment arrives wanting a Docker
  # socket that the prepare step below takes away.
  # CLUSTER_TOPOLOGY=true, for the same reason the bootstrap init needs it and
  # a different cluster: every cluster this run creates is built from a
  # ClusterClass, and a provider installed without the gate refuses them at
  # admission with a message that names the object rather than the install.
  #
  # Not EXP_RUNTIME_SDK: no runtime extension runs here. CAREN is on the
  # bootstrap cluster and has no part in what is measured.
  KUBECONFIG="${WORKLOAD_KUBECONFIG}" env CLUSTER_TOPOLOGY=true \
    "$(clusterctl_for "${CAPI_VERSION}")" init \
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
  kubectl --kubeconfig "${BOOTSTRAP_KUBECONFIG}" -n "${CLUSTER_NAMESPACE}" \
    delete cluster "${CLUSTER_NAME}" --ignore-not-found --wait --timeout=30m
  log "Deleting the kind bootstrap cluster"
  kind delete cluster --name "${BOOTSTRAP_CLUSTER}" --kubeconfig "${BOOTSTRAP_KUBECONFIG}"
}

case "${1:-}" in
  config|bootstrap|clusterclass|create|kubeconfig|install|down) cmd="$1"; shift; "${cmd}" "$@" ;;
  *) sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
