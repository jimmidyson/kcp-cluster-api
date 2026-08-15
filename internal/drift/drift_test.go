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

package drift

import (
	"strings"
	"testing"
)

const exampleRecord = "" +
	"# Drift record\n" +
	"\n" +
	"Base: `281e4e3ed2af1d6852651d69e1207a3073b478c2`\n" +
	"\n" +
	"| Path | Rationale | Upstream proposal |\n" +
	"|---|---|---|\n" +
	"| `internal/contract/version.go` | Makes the resolver pluggable | pending, due 2026-11-13 |\n" +
	"| `controllers/external/metadata.go` | Public seam | pending, due 2026-11-13 |\n"

func TestParseRecord(t *testing.T) {
	rec, err := ParseRecord(exampleRecord)
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}

	if want := "281e4e3ed2af1d6852651d69e1207a3073b478c2"; rec.Base != want {
		t.Errorf("Base = %q, want %q", rec.Base, want)
	}
	if len(rec.Paths) != 2 {
		t.Fatalf("Paths = %v, want 2 entries", rec.Paths)
	}
	if rec.Paths[0] != "internal/contract/version.go" {
		t.Errorf("Paths[0] = %q", rec.Paths[0])
	}
}

// TestParseRecordWithoutBaseFails: a record that does not say what it is
// measured against cannot be checked, and must say so rather than silently
// comparing against nothing.
func TestParseRecordWithoutBaseFails(t *testing.T) {
	const noBase = "# Drift record\n\n| Path |\n|---|\n| `a/b.go` |\n"

	if _, err := ParseRecord(noBase); err == nil {
		t.Error("ParseRecord() succeeded on a record with no base commit; it must fail")
	}
}

// TestUnexpectedPathFails is the check's reason for existing: divergence that
// nobody recorded must be caught automatically, since experience is that it
// arrives through routine activity rather than deliberate decisions.
func TestUnexpectedPathFails(t *testing.T) {
	rec := Record{Base: "abc", Paths: []string{"internal/contract/version.go"}}
	actual := []string{"internal/contract/version.go", "util/sneaky.go"}

	res := Compare(rec, actual)

	if res.OK() {
		t.Fatal("Compare() reported OK despite an unrecorded path")
	}
	if len(res.Unexpected) != 1 || res.Unexpected[0] != "util/sneaky.go" {
		t.Errorf("Unexpected = %v, want [util/sneaky.go]", res.Unexpected)
	}
	if !strings.Contains(res.String(), "util/sneaky.go") {
		t.Errorf("report does not name the unexpected path:\n%s", res)
	}
}

// TestRecordedSetAlonePasses: the agreed set is not itself a failure.
func TestRecordedSetAlonePasses(t *testing.T) {
	rec := Record{Base: "abc", Paths: []string{"a.go", "b.go"}}

	res := Compare(rec, []string{"b.go", "a.go"})

	if !res.OK() {
		t.Errorf("Compare() reported not-OK for exactly the recorded set: %s", res)
	}
}

// TestMissingRecordedPathIsReportedNotFailed: a recorded patch that has
// vanished is worth saying out loud - it may mean an upstream proposal landed
// - but failing on it is deferred (see the spec's Deferred section), so it
// must not turn the check red.
func TestMissingRecordedPathIsReportedNotFailed(t *testing.T) {
	rec := Record{Base: "abc", Paths: []string{"a.go", "b.go"}}

	res := Compare(rec, []string{"a.go"})

	if !res.OK() {
		t.Error("Compare() failed on a missing recorded path; failing on that is deferred, not current behaviour")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "b.go" {
		t.Errorf("Missing = %v, want [b.go]", res.Missing)
	}
	if !strings.Contains(res.String(), "b.go") {
		t.Errorf("report does not mention the missing path:\n%s", res)
	}
}
