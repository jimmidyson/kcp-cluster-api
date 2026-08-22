/*
Copyright 2026 The Kubernetes Authors.

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

package workloaddiag_test

import (
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/workloaddiag"
)

func TestChangeWriterKeepsOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	var lines []string
	w := workloaddiag.NewChangeWriter(func(line string) { lines = append(lines, line) })

	// Two polls of the same table, then one where a row moved on.
	poll := "WORKSPACE\tREADY\nready-1\tyes\nready-2\tno\n"
	for _, table := range []string{poll, poll, "WORKSPACE\tREADY\nready-1\tyes\nready-2\tyes\n"} {
		if _, err := io.WriteString(w, table); err != nil {
			t.Fatalf("writing a table: %v", err)
		}
	}

	want := []string{"WORKSPACE\tREADY", "ready-1\tyes", "ready-2\tno", "ready-2\tyes"}
	if !slices.Equal(lines, want) {
		t.Errorf("kept %q, want %q", lines, want)
	}
}

func TestChangeWriterWaitsForAWholeLine(t *testing.T) {
	t.Parallel()

	var lines []string
	w := workloaddiag.NewChangeWriter(func(line string) { lines = append(lines, line) })

	// A tabwriter flushes in pieces, so a row reaches this writer in pieces.
	for _, piece := range []string{"ready-1", "\tprovisi", "oned\nready-2\tready\n", "trailing with no newline"} {
		if _, err := io.WriteString(w, piece); err != nil {
			t.Fatalf("writing a piece: %v", err)
		}
	}

	want := []string{"ready-1\tprovisioned", "ready-2\tready"}
	if !slices.Equal(lines, want) {
		t.Errorf("kept %q, want %q", lines, want)
	}
}

func TestChangeWriterReportsEverythingItWasGiven(t *testing.T) {
	t.Parallel()

	// An io.Writer that under-reports what it wrote makes its caller fail with
	// a short write, which would turn a progress report into a run-ending
	// error.
	w := workloaddiag.NewChangeWriter(func(string) {})
	for _, in := range []string{"", "no newline", "a\nb\n"} {
		n, err := io.WriteString(w, in)
		if err != nil {
			t.Fatalf("writing %q: %v", in, err)
		}
		if n != len(in) {
			t.Errorf("writing %q reported %d bytes, want %d", in, n, len(in))
		}
	}
	if _, err := fmt.Fprintf(w, "formatted\n"); err != nil {
		t.Errorf("writing through fmt: %v", err)
	}
}
