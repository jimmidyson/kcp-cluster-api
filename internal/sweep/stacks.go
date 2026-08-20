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

package sweep

import (
	"bytes"
	"fmt"
	"runtime/pprof"
	"sort"
	"strings"
)

// Stacks is how many goroutines were sitting in each distinct stack when a
// sample was taken.
//
// # Why a sample carries this at all
//
// A retention figure is a difference between two samples, so a profile written
// when the difference is *reported* describes neither of them: by then the
// sweep has finished and every workspace has gone. Three separate CI failures
// were argued about from the number alone, and the profile added to settle
// them was written at the end and answered a question nobody had asked.
//
// Keeping the stacks on the sample means the two ends of the comparison can be
// subtracted from each other, which names the goroutines that a departure did
// not give back instead of listing everything still running.
type Stacks map[string]int

// StackDelta is one stack that grew between two samples.
type StackDelta struct {
	// Stack is the goroutine's stack, innermost frame first.
	Stack string
	// Before and After are the counts in the two samples.
	Before, After int
}

// Growth is how many goroutines the stack gained.
func (d StackDelta) Growth() int { return d.After - d.Before }

// takeStacks profiles the process now, grouped by stack.
//
// pprof's debug=1 form is the grouped one: a count and a stack per record,
// rather than one record per goroutine. That is what makes subtracting two
// profiles meaningful — 90 controller workers parked on the same queue are one
// line whose count moves, not 90 lines that all have to be matched up.
func takeStacks() Stacks {
	p := pprof.Lookup("goroutine")
	if p == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		return nil
	}
	return parseStacks(buf.String())
}

// parseStacks reads a debug=1 goroutine profile into counts by stack.
//
// The format is a header line, then records separated by blank lines. Each
// record starts "N @ 0x... 0x..." and is followed by tab-indented frame lines.
// The addresses differ between two profiles of the same program only in
// uninteresting ways, so the frames are what a record is keyed by.
func parseStacks(profile string) Stacks {
	out := Stacks{}
	for _, record := range strings.Split(profile, "\n\n") {
		lines := strings.Split(strings.TrimSpace(record), "\n")
		if len(lines) < 2 {
			continue
		}
		var count int
		if _, err := fmt.Sscanf(lines[0], "%d @", &count); err != nil {
			continue // The header, or something else that is not a record.
		}
		var frames []string
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "#") {
				continue
			}
			// "#\t0x1234\tpkg.Func+0x10\t/path/file.go:42" — keep the symbol
			// and drop both the program counter and the +offset after the
			// symbol. Neither says anything a reader wants, and the offset
			// makes the same call site read differently between two builds.
			fields := strings.Split(line, "\t")
			if len(fields) < 3 {
				continue
			}
			symbol := strings.TrimSpace(fields[2])
			if plus := strings.LastIndex(symbol, "+0x"); plus > 0 {
				symbol = symbol[:plus]
			}
			where := ""
			if len(fields) > 3 {
				where = " " + strings.TrimSpace(fields[3])
			}
			frames = append(frames, symbol+where)
		}
		if len(frames) == 0 {
			continue
		}
		out[strings.Join(frames, "\n")] += count
	}
	return out
}

// StacksGrown lists the stacks that hold more goroutines in after than in
// before, largest growth first.
//
// Only growth: a stack that shrank gave its goroutines back, which is what was
// supposed to happen, and listing it would bury the ones that did not.
func StacksGrown(before, after Stacks) []StackDelta {
	var out []StackDelta
	for stack, n := range after {
		if was := before[stack]; n > was {
			out = append(out, StackDelta{Stack: stack, Before: was, After: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Growth() != out[j].Growth() {
			return out[i].Growth() > out[j].Growth()
		}
		return out[i].Stack < out[j].Stack
	})
	return out
}

// FormatStacksGrown renders the growth between two samples for a test log.
//
// limit caps how many stacks are printed; the rest are summarised, because a
// profile that scrolls past the top of the log is one nobody reads. Pass 0 for
// no limit.
func FormatStacksGrown(before, after Sample, limit int) string {
	grown := StacksGrown(before.Stacks, after.Stacks)
	var b strings.Builder
	fmt.Fprintf(&b, "goroutines held at %q (%d) that were not held at %q (%d), by stack:\n",
		after.Label, after.Goroutines, before.Label, before.Goroutines)
	if len(grown) == 0 {
		if before.Stacks == nil || after.Stacks == nil {
			b.WriteString("\n\t(no profile was captured for one of these samples)\n")
			return b.String()
		}
		b.WriteString("\n\t(no stack grew — the difference is not attributable to any goroutine still running)\n")
		return b.String()
	}
	shown := grown
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, d := range shown {
		fmt.Fprintf(&b, "\n+%d (%d -> %d) @\n", d.Growth(), d.Before, d.After)
		for _, frame := range strings.Split(d.Stack, "\n") {
			fmt.Fprintf(&b, "\t%s\n", frame)
		}
	}
	if len(grown) > len(shown) {
		var rest int
		for _, d := range grown[len(shown):] {
			rest += d.Growth()
		}
		fmt.Fprintf(&b, "\n\t… and %d more stack(s), %d goroutine(s) in total\n", len(grown)-len(shown), rest)
	}
	return b.String()
}
