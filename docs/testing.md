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

### `envtest.Environment`'s workspace parameter

`kcp` rejects API requests made against its bare server root (the "base",
cluster-unaware config) — every request must target one specific logical
cluster. `envtest.Environment(t, workspace)` returns a REST config already
scoped to `workspace` (a context name from the server's kubeconfig, e.g.
`"root"` — pass `""` to default to `"root"`), which is what plain
client-go and controller-runtime clients expect.

## Running everything

```sh
task verify   # build, lint, unit and integration tests
```

[sdk-testing]: https://github.com/kcp-dev/sdk/tree/main/testing
