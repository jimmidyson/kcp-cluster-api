# Testing: TDD

All new behaviour is developed test-first:

1. **Red** — write a failing test that describes the behavior you want.
2. **Green** — write the minimal code to make it pass.
3. **Refactor** — clean up with the test still passing.

Every change needs tests at both of the tiers below, whichever apply to it.
A PR that adds or changes behaviour without accompanying tests
is incomplete.

## Unit tests

Plain Go tests, colocated with the code they exercise (`foo.go` /
`foo_test.go`, same package). No real processes, no real API server — use
fakes (`sigs.k8s.io/controller-runtime/pkg/client/fake`,
`k8s.io/client-go/kubernetes/fake`) or plain unit logic. These should be
fast enough to run on every save.

Run them with:

```sh
task test:unit
```

## Integration tests: via KCP envtest

Controller-runtime's usual `envtest` package only stands up a vanilla
`kube-apiserver` + `etcd`. That has no concept of KCP's logical
clusters/workspaces, so it cannot validate anything KCP-specific — a
reconciler that behaves correctly against vanilla envtest can still be
completely broken against real KCP semantics (workspace-scoped requests,
`/clusters/<path>` routing, etc.).

Instead, integration tests use `test/integration/envtest`
(this repo's package, not controller-runtime's), which starts a **real kcp
server process** via [`github.com/kcp-dev/sdk/testing`][sdk-testing] — the
same fixture kcp itself uses for its own integration tests. This is the
"KCP envtest" referred to elsewhere in this repo's docs.

Usage:

```go
package mypackage_test

import (
    "testing"

    "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

func TestSomething(t *testing.T) {
    cfg := envtest.Environment(t, "") // REST config for the root workspace
    // build a client/manager from cfg and exercise real KCP-aware behavior
}
```

Integration test files live under `test/integration/` and carry the
`integration` build tag (`//go:build integration`), so they're excluded
from `go test ./...` and only run explicitly:

```sh
task test:integration
```

That target downloads a pinned `kcp` server binary (see `KCP_VERSION` in
`Taskfile.yaml`) into `bin/` and puts it on `PATH` before running the
tagged tests — `go install github.com/kcp-dev/kcp/cmd/kcp@<version>` does
not work here, because kcp's `go.mod` carries `replace` directives and `go
install pkg@version` refuses to build a non-main module that has any.

### One kcp server per package, two packages at a time

Every package under `test/integration/` starts its own kcp server with an
embedded etcd, and `go test` runs packages in parallel — `-p`, which
defaults to `GOMAXPROCS`. On a four-core runner that brings four servers up
at once, and a server that does not reach readiness inside the fixture's
timeout fails its whole package before any test body runs.

`task test:integration:kcp` therefore passes `-p 2`. Two rather than one
keeps most of the wall-clock saving — the packages sum to about eight
minutes run one at a time — while halving how many servers compete to
start.

**If bring-up starts failing again at two, the next step is `-p 1`, not a
re-run.** A readiness timeout, or a discovery error like `the server is
currently unable to handle the request`, is contention rather than a defect
in whichever package happened to report it — the failing package moves
around between runs, which is the tell.

Nothing under `test/integration/` calls `t.Parallel()`, so tests within a
package already run one at a time.

### `envtest.Environment`'s workspace parameter

`kcp` rejects API requests made against its bare server root (the "base",
cluster-unaware config) — every request must target one specific logical
cluster. `envtest.Environment(t, workspace)` returns a REST config already
scoped to `workspace` (a context name from the server's kubeconfig, e.g.
`"root"` — pass `""` to default to `"root"`), which is what plain
client-go and controller-runtime clients expect.

### When a container-backed cluster does not come up

`test/integration/dockerbackend` waits 20 minutes for two real clusters to
reach ready. When one does not, the management side has already said
everything it knows — the Machine is `NotReady`, and kubelet's reason is
usually `cni plugin not initialized` — and the reason is inside the workload
cluster, which the fixture is about to tear down.

So the failure path dumps it first, per workspace, before the test fails:

- every Node's conditions, with the Ready condition's reason and message;
- the DaemonSets in `kube-system`, and every pod there that is not ready;
- container logs for those pods and for the CNI's, including the log of a
  container that died before the one running now;
- and two questions its API server cannot answer, asked of the control plane
  container itself: what is in `/etc/cni/net.d`, and what kubelet's journal
  says. A CNI that reported its pods ready and wrote no configuration file
  looks identical from the API server and is obvious from those two.

The dump is written to `ARTIFACT_DIR` when set and `bin/` otherwise, as
`diagnostics-<TestName>.md`, and repeated in the test log. `ARTIFACT_DIR` is
the kcp fixture's own variable — it puts `kcp.log`, the audit log and its
object dumps there — so one setting collects both; `task
test:integration:container-runtime` sets it to `bin/artifacts`.

CI uploads that directory as the `verify-artifacts` artifact when a run fails,
minus the audit logs, which are large and only readable against a specific
question. Re-run the suite locally with `ARTIFACT_DIR` set to keep them.

**Suite log lines carry a wall-clock stamp.** `go test` buffers a package's
output and prints it when the test fails, so every line otherwise appears to
have happened at the moment the run gave up — and the interval between "the
CNI reported itself installed" and "the Node never came up" is usually the
question being asked.

The wait's own status tables are logged too, deduplicated: the demo re-renders
them every five seconds, so a twenty minute wait would otherwise repeat the
same rows 240 times. Each row is logged the first time it says what it says,
which makes the log the timeline of what reached which state, and when.

## Running everything

```sh
task verify   # build, lint, unit and integration tests
```

[sdk-testing]: https://github.com/kcp-dev/sdk/tree/main/testing
