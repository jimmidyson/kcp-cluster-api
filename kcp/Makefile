# Test targets for kcp/-specific code.
#
# `kcp/` is its own Go module (see kcp/go.mod) so it can depend on
# github.com/kcp-dev/sdk without touching the root module. See
# kcp/docs/testing.md for the TDD policy these targets exist to enforce.

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

KCP_VERSION ?= v0.32.3
LOCALBIN ?= $(CURDIR)/bin
KCP_OS ?= $(shell go env GOOS)
KCP_ARCH ?= $(shell go env GOARCH)

.PHONY: test
test: test-unit test-integration

.PHONY: test-unit
test-unit:
	go test ./...

# kcp-binary downloads the released kcp server binary into $(LOCALBIN).
#
# `go install github.com/kcp-dev/kcp/cmd/kcp@$(KCP_VERSION)` does not work:
# kcp's go.mod carries replace directives (for its k8s.io/* fork and its
# staging/ submodules), and `go install pkg@version` refuses to build a
# module that isn't the main module if its go.mod has any replace
# directives at all. Using the prebuilt release archive sidesteps that.
.PHONY: kcp-binary
kcp-binary:
	mkdir -p $(LOCALBIN)
	curl -sSL "https://github.com/kcp-dev/kcp/releases/download/$(KCP_VERSION)/kcp_$(patsubst v%,%,$(KCP_VERSION))_$(KCP_OS)_$(KCP_ARCH).tar.gz" \
		| tar -xzf - -C $(LOCALBIN) --strip-components=1 bin/kcp

.PHONY: test-integration
test-integration: kcp-binary
	PATH="$(LOCALBIN):$$PATH" go test -tags=integration -timeout=15m ./test/integration/...
