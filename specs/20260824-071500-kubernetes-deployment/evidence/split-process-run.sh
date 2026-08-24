#!/bin/sh
# The deployed topology, minus Kubernetes: kcp with the generated PKI, each
# manager as its own process with the kubeconfig its Deployment mounts, and the
# demo with its manager half switched off.
set -eu
S="$(dirname "$0")"
S="$(cd "$S" && pwd)"
KCP="${KCP:-$(git rev-parse --show-toplevel)/bin/kcp}"   # task tools puts it there
REPO="${REPO:-$(git rev-parse --show-toplevel)}"

# Anything left from a previous run holds the ports this one needs.
pkill -f "$S/bin/" 2>/dev/null || true
pkill -x kcp 2>/dev/null || true
sleep 2

stop_all() {
  pkill -f "$S/bin/" 2>/dev/null || true
  pkill -x kcp 2>/dev/null || true
}
trap stop_all EXIT INT TERM

start() {
  name=$1; shift
  "$@" > "$S/$name.log" 2>&1 &
  echo $! > "$S/$name.pid"
  echo "started $name (pid $(cat "$S/$name.pid"))"
}

start kcp "$KCP" start \
  --root-directory "$S/state" \
  --secure-port=6443 \
  --embedded-etcd-client-port=2379 --embedded-etcd-peer-port=2380 \
  --shard-base-url=https://localhost:6443 \
  --shard-external-url=https://localhost:6443 \
  --shard-virtual-workspace-url=https://localhost:6443 \
  --tls-cert-file="$S/serving.crt" \
  --tls-private-key-file="$S/serving.key" \
  --client-ca-file="$S/client-ca.crt"

# Wait for the shard the way the readiness probe does.
i=0
while [ $i -lt 120 ]; do
  if curl -sf -o /dev/null --noproxy '*' --cacert "$S/serving.crt" https://localhost:6443/readyz; then break; fi
  i=$((i+1)); sleep 1
done
echo "kcp ready after ${i}s"

# Every manager starts before anything has published an APIExport, which is
# what a Deployment does. --startup-timeout is what makes that survivable.
start workspace-manager "$S/bin/workspace-manager" \
  --kubeconfig "$S/base.kubeconfig" --provider-workspace=root --startup-timeout=10m

port=9440
for m in core:cluster-api-core bootstrap:cluster-api-bootstrap-kubeadm controlplane:cluster-api-controlplane-kubeadm; do
  name=${m%%:*}; export=${m##*:}
  case $name in
    core) bin=core-manager ;;
    bootstrap) bin=kubeadm-bootstrap-manager ;;
    controlplane) bin=kubeadm-control-plane-manager ;;
  esac
  port=$((port+1))
  start "$name" "$S/bin/$bin" \
    --kubeconfig "$S/provider.kubeconfig" \
    --endpoint-slice-name="$export" \
    --startup-timeout=10m \
    --health-addr=":$port" \
    --metrics-addr=":$((port+100))"
done

POD_IP=127.0.0.1 start dev-infrastructure "$S/bin/dev-infrastructure-manager" \
  --kubeconfig "$S/provider.kubeconfig" \
  --endpoint-slice-name=cluster-api-dev-infrastructure \
  --startup-timeout=10m \
  --health-addr=":9450" --metrics-addr=":9550"

# The demo Job.
set +e
# PATH is deliberately empty and KO_DATA_PATH is set: this is the container's
# situation, where publishing the APIExports has to read CRD manifests with no
# Go toolchain to resolve the modules with.
env -i PATH=/nonexistent HOME="$S" \
  KO_DATA_PATH=$REPO/cmd/demo/kodata \
  "$S/bin/demo" \
  --kcp-kubeconfig "$S/base.kubeconfig" \
  --kcp-kubeconfig-context=base \
  --no-manager \
  --parent=root \
  --backend=inmemory \
  --workspace-kubeconfig-dir="$S/out" \
  --workspaces=2 --users=alice,bob --clusters=1 \
  --control-plane-machines=1 --worker-machines=1 \
  --timeout=10m
demo_status=$?
set -e
echo "demo exited $demo_status"
exit $demo_status
