/*
Copyright 2025 The Kubernetes Authors.

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

package scaleharness

import (
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Workspace is one provisioned workspace: its identity, and a client scoped to
// it.
//
// The name is not decoration. Delivery latency is attributed per workspace, and
// the controller that reports a delivery knows only which workspace it belongs
// to — so without an identity travelling alongside the client, a measurement
// could not be matched to the mutation that caused it.
type Workspace struct {
	Name   string
	Client client.Client
}

// Measurement is what one swept point cost.
//
// Heap rather than resident size, deliberately (FR-036): Go returns memory to
// the OS lazily and fragments, so resident size is not a clean function of what
// the process is holding. Live heap is the quantity that scales with
// workspaces; converting it to a resident-size budget is a separate step with
// its own stated multiplier.
type Measurement struct {
	Workspaces   int           `json:"workspaces"`
	HeapBytes    uint64        `json:"heapBytes"`
	Goroutines   int           `json:"goroutines"`
	LoadDuration time.Duration `json:"loadDuration"`
	Events       int           `json:"events"`

	// ProcessBytes is everything the Go runtime has obtained from the OS
	// (`MemStats.Sys`), and StackBytes the goroutine-stack part of it.
	//
	// Live heap is what a shard is *holding*; this is what it *occupies*, and
	// the two diverge by more than a constant. Stacks in particular scale with
	// workspace count and are absent from HeapAlloc entirely, so a container
	// sized from heap alone would be under-provisioned by a term that grows
	// with the fleet.
	//
	// Neither is resident set size — the runtime returns pages to the OS
	// lazily, so Sys is an over-estimate of RSS at any instant and the right
	// direction to err in when setting a limit.
	ProcessBytes uint64 `json:"processBytes,omitempty"`
	StackBytes   uint64 `json:"stackBytes,omitempty"`

	// DeliveryP50 and DeliveryP99 are how long an event took to travel from
	// the write to the controller that wanted it.
	//
	// This, not LoadDuration, is where a fan-out cost appears. A write returns
	// once the API server accepts it, so the dispatch through every registered
	// listener happens after the writer has stopped looking.
	DeliveryP50 time.Duration `json:"deliveryP50,omitempty"`
	DeliveryP99 time.Duration `json:"deliveryP99,omitempty"`

	// DeliveriesMissed counts events that never reached a controller within
	// the timeout. Non-zero means dispatch is not keeping up, which is a
	// finding in itself and makes the percentiles above a lower bound.
	DeliveriesMissed int `json:"deliveriesMissed,omitempty"`
}
