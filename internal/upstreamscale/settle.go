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

package upstreamscale

import (
	"context"
	"fmt"
	"math"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Settled reports whether two consecutive samples agree within tolerance on
// every component's goroutine count.
//
// # Why a baseline has to wait for this
//
// A manager does not reach its resting size the moment its pod is Running. It
// opens caches, starts informers and fills worker pools, and until that
// finishes its goroutine count is a number on the way up.
//
// The 25x1 run caught the kubeadm control plane manager at 35 goroutines with
// no fleet. Three minutes later, still with no fleet, the same pod reported
// 375. The baseline was of a starting process, so the rung's apparent cost
// carried 340 goroutines the manager was always going to open — and that run
// reported half again the per-rung cost the settled runs did, for a fleet the
// same size.
//
// The baseline is the zero point of every figure in the report, so it is worth
// waiting for. Goroutines rather than heap, because heap is a sawtooth even in
// a settled process and would never agree with itself.
func Settled(prev, cur []deployedscale.ComponentSample, tolerance float64) bool {
	if len(prev) == 0 || len(prev) != len(cur) {
		return false
	}
	before := map[string]int{}
	for _, c := range prev {
		before[c.Component] = c.Process.Goroutines
	}
	for _, c := range cur {
		was, ok := before[c.Component]
		if !ok {
			// A component in one sample and not the other is a pod that was
			// rolling; calling that settled takes the baseline mid-roll.
			return false
		}
		if change(was, c.Process.Goroutines) > tolerance {
			return false
		}
	}
	return true
}

// change is the relative movement between two counts, guarding the zero a
// process that has not started yet reports.
func change(was, now int) float64 {
	if was == 0 {
		if now == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return math.Abs(float64(now-was)) / float64(was)
}

// SettleResult is what the wait found, and it is reported either way: a
// baseline that never stopped moving is a caveat on every number derived from
// it, which is worth saying and not worth failing a run over.
type SettleResult struct {
	Settled bool          `json:"settled"`
	Waited  time.Duration `json:"waited"`
	// Worst and WorstChange name the component still moving most when the wait
	// ended, so a reader knows which figures to distrust.
	Worst       string  `json:"worst,omitempty"`
	WorstChange float64 `json:"worstChange,omitempty"`
}

// Describe is the fact the report carries about its own zero point.
func (r SettleResult) Describe() string {
	if r.Settled {
		return fmt.Sprintf("the controllers' goroutine counts stopped moving after %s, so the "+
			"baseline is of started managers rather than starting ones", r.Waited.Round(time.Second))
	}
	return fmt.Sprintf("**the baseline did not settle**: after %s %s was still moving by %.0f%% "+
		"between samples, so every figure measured against this baseline is suspect",
		r.Waited.Round(time.Second), r.Worst, 100*r.WorstChange)
}

// WaitForSettled samples until two consecutive reads agree, or the timeout.
func WaitForSettled(ctx context.Context, s *Sampler, cl client.Client, controllers []Controller,
	tolerance float64, timeout, poll time.Duration,
) (SettleResult, error) {
	started := time.Now()
	deadline := started.Add(timeout)

	prev, _, err := s.Sample(ctx, cl, controllers)
	if err != nil {
		return SettleResult{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return SettleResult{Waited: time.Since(started)}, ctx.Err()
		case <-time.After(poll):
		}

		cur, _, err := s.Sample(ctx, cl, controllers)
		if err != nil {
			return SettleResult{Waited: time.Since(started)}, err
		}
		if Settled(prev, cur, tolerance) {
			return SettleResult{Settled: true, Waited: time.Since(started)}, nil
		}
		if time.Now().After(deadline) {
			worst, by := worstMover(prev, cur)
			return SettleResult{Waited: time.Since(started), Worst: worst, WorstChange: by}, nil
		}
		prev = cur
	}
}

func worstMover(prev, cur []deployedscale.ComponentSample) (string, float64) {
	before := map[string]int{}
	for _, c := range prev {
		before[c.Component] = c.Process.Goroutines
	}
	worst, by := "", 0.0
	for _, c := range cur {
		was, ok := before[c.Component]
		if !ok {
			return c.Component, math.Inf(1)
		}
		if moved := change(was, c.Process.Goroutines); moved > by {
			worst, by = c.Component, moved
		}
	}
	return worst, by
}
