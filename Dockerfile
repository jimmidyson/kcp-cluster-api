# The image this project deploys from: every binary in the repository, the
# pinned kcp server, and the CRD manifests the exports are published from.
#
# One image rather than one per binary. The managers are one deployment each
# and the shard is another, but they are all built from this commit and pinned
# together by it - so a tag names a state of the repository, and there is one
# thing to build, push and load into a kind cluster instead of six.
#
# Build it with `task image`.

ARG GO_VERSION=1.26.3
ARG KCP_VERSION=v0.32.3

FROM --platform=${BUILDPLATFORM} golang:${GO_VERSION} AS build

WORKDIR /src

# The module graph first, so that a change to the code does not re-download it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binaries, because the base image below has no libc. TARGETARCH comes
# from the builder, so `docker buildx build --platform` cross-compiles without
# a cross toolchain: the compile is native and only the output differs.
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    for cmd in core-manager kubeadm-bootstrap-manager kubeadm-control-plane-manager \
               dev-infrastructure-manager workspace-manager demo; do \
      CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
        go build -trimpath -ldflags='-s -w' -o "/out/bin/${cmd}" "./cmd/${cmd}"; \
    done

# The CRD manifests, copied out of the same module cache the binaries were
# compiled against.
#
# This is what makes publishing an APIExport work in a container. The paths are
# resolved from the build list at run time (internal/kcpfixtures.ModuleDir), and
# a container has neither a Go toolchain nor a module cache to resolve them
# from - so they are copied here, in this build, from the pinned versions, and
# the image points KCP_CLUSTER_API_MANIFEST_ROOT at them.
RUN set -eux; \
    for module in sigs.k8s.io/cluster-api \
                  sigs.k8s.io/cluster-api/test \
                  github.com/nutanix-cloud-native/cluster-api-provider-nutanix; do \
      go mod download "${module}"; \
      dir="$(go list -m -f '{{.Dir}}' "${module}")"; \
      [ -n "${dir}" ]; \
      find "${dir}" -type f -path '*/config/crd/*' -name '*.yaml' | while read -r manifest; do \
        rel="${manifest#"${dir}"/}"; \
        install -D -m 0444 "${manifest}" "/out/manifests/${module}/${rel}"; \
      done; \
    done

# The kcp server, at the version `task tools` installs for a local run. Same
# release, same URL, same pin: an installation and a laptop run the same shard.
ARG KCP_VERSION
RUN set -eux; \
    curl -sSfL "https://github.com/kcp-dev/kcp/releases/download/${KCP_VERSION}/kcp_${KCP_VERSION#v}_linux_${TARGETARCH}.tar.gz" \
      | tar -xzf - -C /out/bin --strip-components=1 bin/kcp; \
    chmod 0555 /out/bin/kcp

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/bin/ /usr/local/bin/
COPY --from=build /out/manifests/ /manifests/

# Where the CRD manifests went. Reading them is how an APIExport is published;
# see internal/kcpfixtures.ManifestRootEnv.
ENV KCP_CLUSTER_API_MANIFEST_ROOT=/manifests

# 65532 is distroless' nonroot user, and is what the generated pod security
# context asks Kubernetes for.
USER 65532:65532

# No entrypoint. Every deployment names the binary it runs, because this image
# holds six of them and none of them is the obvious default.
