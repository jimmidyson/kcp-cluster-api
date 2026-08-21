//go:build manifestdeps

/*
Copyright 2026 The kcp-cluster-api Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file is never built. It exists so that `go mod tidy` keeps modules this
// project reads *manifests* from but imports no code from.
//
// ModuleDir resolves a module's directory with `go list -m`, which reports only
// modules in the build list. A module nothing imports is not in the build list
// after a tidy, so the CRD paths that resolve against it stop resolving — and
// they stop resolving at run time, in whatever published the export, rather
// than at compile time where it would be obvious.
//
// A blank import under a build tag is the ordinary Go answer: `go mod tidy`
// considers every build configuration, so the requirement is kept, while no
// binary compiles the package or the SDK behind it.
//
// Cluster API is not listed here because this project imports its code as well
// as its manifests. CAPX is listed because today it publishes an export and
// nothing more — when a Nutanix manager exists and imports its reconcilers,
// this entry becomes redundant and should be removed rather than left as a
// second, weaker statement of the same dependency.
package kcpfixtures

import (
	_ "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
)
