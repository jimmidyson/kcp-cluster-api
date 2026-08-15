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

// Package drift compares the patches this project carries against Cluster API
// with the set recorded in DRIFT.md.
//
// The project's value depends on that set trending to zero, and experience is
// that divergence arrives through routine activity - a dependency bot, a CI
// fix - rather than through deliberate decisions. So it is measured rather
// than trusted.
package drift

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Record is the parsed content of DRIFT.md.
type Record struct {
	// Base is the upstream commit the fork branch is cut from. Without it
	// there is nothing to measure divergence against.
	Base string
	// Paths are the files the fork is permitted to differ in.
	Paths []string
}

var (
	baseRe = regexp.MustCompile("(?m)^Base:\\s*`([0-9a-f]{7,40})`")
	pathRe = regexp.MustCompile("(?m)^\\|\\s*`([^`]+)`\\s*\\|")
)

// ParseRecord reads a drift record.
func ParseRecord(md string) (Record, error) {
	m := baseRe.FindStringSubmatch(md)
	if m == nil {
		return Record{}, fmt.Errorf("drift record has no base commit: expected a line like \"Base: `<sha>`\"; " +
			"a record that does not say what it is measured against cannot be checked")
	}

	var paths []string
	for _, pm := range pathRe.FindAllStringSubmatch(md, -1) {
		paths = append(paths, strings.TrimSpace(pm[1]))
	}
	if len(paths) == 0 {
		return Record{}, fmt.Errorf("drift record lists no paths; an empty record is indistinguishable from an unwritten one")
	}

	return Record{Base: m[1], Paths: paths}, nil
}

// Result is the outcome of comparing a record against reality.
type Result struct {
	// Unexpected are paths the fork differs in that the record does not
	// permit. These fail the check.
	Unexpected []string
	// Missing are recorded paths the fork no longer differs in. Reported,
	// but not a failure: failing on this direction is deferred (see the
	// feature spec's Deferred section). It usually means an upstream
	// proposal landed and the patch can be retired from the record.
	Missing []string
}

// OK reports whether the fork's divergence is within the recorded set.
func (r Result) OK() bool { return len(r.Unexpected) == 0 }

// Compare checks the actual differing paths against the record.
func Compare(rec Record, actual []string) Result {
	recorded := make(map[string]bool, len(rec.Paths))
	for _, p := range rec.Paths {
		recorded[p] = true
	}
	seen := make(map[string]bool, len(actual))
	for _, p := range actual {
		seen[p] = true
	}

	var res Result
	for p := range seen {
		if !recorded[p] {
			res.Unexpected = append(res.Unexpected, p)
		}
	}
	for p := range recorded {
		if !seen[p] {
			res.Missing = append(res.Missing, p)
		}
	}
	sort.Strings(res.Unexpected)
	sort.Strings(res.Missing)
	return res
}

// String renders a human-readable report.
func (r Result) String() string {
	var b strings.Builder

	if len(r.Unexpected) > 0 {
		b.WriteString("unrecorded divergence from upstream:\n")
		for _, p := range r.Unexpected {
			fmt.Fprintf(&b, "  %s\n", p)
		}
		b.WriteString("\nEach carried patch must be justified in DRIFT.md before it is accepted,\n")
		b.WriteString("with the upstream proposal that will make it unnecessary.\n")
	}

	if len(r.Missing) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("recorded patches the fork no longer carries:\n")
		for _, p := range r.Missing {
			fmt.Fprintf(&b, "  %s\n", p)
		}
		b.WriteString("\nNot a failure. If an upstream proposal landed, retire the entry from\n")
		b.WriteString("DRIFT.md so the record keeps matching reality.\n")
	}

	if b.Len() == 0 {
		b.WriteString("divergence matches DRIFT.md exactly\n")
	}
	return b.String()
}
