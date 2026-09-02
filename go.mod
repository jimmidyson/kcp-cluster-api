module github.com/jimmidyson/kcp-cluster-api

go 1.26.3

require (
	github.com/containerd/errdefs v1.0.0
	github.com/go-logr/logr v1.4.4
	github.com/kcp-dev/apimachinery/v2 v2.32.3
	github.com/kcp-dev/logicalcluster/v3 v3.0.5
	github.com/kcp-dev/multicluster-provider v0.8.0
	github.com/kcp-dev/sdk v0.32.3
	github.com/moby/moby/client v0.5.1
	github.com/nutanix-cloud-native/cluster-api-provider-nutanix v1.10.3
	github.com/onsi/gomega v1.42.1
	github.com/spf13/pflag v1.0.10
	k8s.io/api v0.36.3
	k8s.io/apiextensions-apiserver v0.36.3
	k8s.io/apimachinery v0.36.3
	k8s.io/client-go v0.36.3
	k8s.io/component-base v0.36.3
	k8s.io/klog/v2 v2.140.0
	k8s.io/utils v0.0.0-20260319190234-28399d86e0b5
	sigs.k8s.io/cluster-api v1.15.0-kcp.11
	sigs.k8s.io/cluster-api/api v1.15.0-kcp.11
	sigs.k8s.io/cluster-api/test v1.15.0-kcp.11
	sigs.k8s.io/controller-runtime v0.24.1
	sigs.k8s.io/multicluster-runtime v0.24.1
)

// Cluster API is consumed as an ordinary versioned dependency, resolved to
// this project's patched fork. The fork carries the patches recorded in
// DRIFT.md and is otherwise upstream at its base commit.
//
// These replace directives do not propagate: Go honours replace only in the
// main module, and the fork keeps the module path sigs.k8s.io/cluster-api.
// Anything that consumes this module must restate all three at matching
// versions, or it resolves genuine upstream Cluster API instead. See
// docs/adr-0004-scaling-to-many-provider-forks.md.
//
// All three modules must be pinned together: api/ and test/ are separate Go
// modules inside the Cluster API repository, resolved by tag prefix, and a
// partial pin fails at dependency resolution rather than at compile time.
replace sigs.k8s.io/cluster-api => github.com/jimmidyson/cluster-api v1.15.0-kcp.12

replace sigs.k8s.io/cluster-api/api => github.com/jimmidyson/cluster-api/api v1.15.0-kcp.12

replace sigs.k8s.io/cluster-api/test => github.com/jimmidyson/cluster-api/test v1.15.0-kcp.12

require (
	cel.dev/expr v0.25.2 // indirect
	dario.cat/mergo v1.0.1 // indirect
	github.com/Masterminds/goutils v1.1.1 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/Masterminds/sprig/v3 v3.3.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/ajeddeloh/go-json v0.0.0-20200220154158-5ae607161559 // indirect
	github.com/alecthomas/units v0.0.0-20240927000941-0f3dac36c52b // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/coredns/caddy v1.1.1 // indirect
	github.com/coredns/corefile-migration v1.0.34 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/egymgmbh/go-prefix-writer v0.0.0-20180609083313-7326ea162eca // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/flatcar/container-linux-config-transpiler v0.9.4 // indirect
	github.com/flatcar/ignition v0.36.2 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/jsonreference v0.21.5 // indirect
	github.com/go-openapi/swag v0.26.0 // indirect
	github.com/go-openapi/swag/cmdutils v0.26.0 // indirect
	github.com/go-openapi/swag/conv v0.26.0 // indirect
	github.com/go-openapi/swag/fileutils v0.26.0 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/go-openapi/swag/jsonutils v0.26.0 // indirect
	github.com/go-openapi/swag/loading v0.26.0 // indirect
	github.com/go-openapi/swag/mangling v0.26.0 // indirect
	github.com/go-openapi/swag/netutils v0.26.0 // indirect
	github.com/go-openapi/swag/stringutils v0.26.0 // indirect
	github.com/go-openapi/swag/typeutils v0.26.0 // indirect
	github.com/go-openapi/swag/yamlutils v0.26.0 // indirect
	github.com/gobuffalo/flect v1.0.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/cel-go v0.29.0 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus v1.1.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/huandu/xstrings v1.5.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/moby/api v1.55.0 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nutanix-cloud-native/prism-go-client v0.8.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/valyala/fastjson v1.6.10 // indirect
	github.com/vincent-petithory/dataurl v1.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.etcd.io/etcd/api/v3 v3.6.14 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.6.14 // indirect
	go.etcd.io/etcd/client/v3 v3.6.14 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.65.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.65.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	go4.org v0.0.0-20201209231011-d4a079459e60 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/term v0.44.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.5.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiserver v0.36.3 // indirect
	k8s.io/cluster-bootstrap v0.36.3 // indirect
	k8s.io/kube-openapi v0.0.0-20260414162039-ec9c827d403f // indirect
	k8s.io/streaming v0.36.3 // indirect
	sigs.k8s.io/apiserver-network-proxy/konnectivity-client v0.34.0 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/kind v0.32.0 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/nutanix-cloud-native/cluster-api-provider-nutanix => github.com/jimmidyson/cluster-api-provider-nutanix v1.11.0-kcp.2
