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

package workloaddiag

import (
	"bytes"
	"io"
	"strings"
)

// changeWriter passes on each distinct line it is written, once.
type changeWriter struct {
	emit func(line string)
	seen map[string]bool

	// partial holds the tail of a write that did not end on a line boundary.
	partial bytes.Buffer
}

// NewChangeWriter returns a writer that calls emit for every line it has not
// been written before, and drops the repeats.
//
// It is for a progress report that is re-rendered on a timer. A run that polls
// a status table every five seconds writes the same rows 240 times over twenty
// minutes: logging all of it buries the run, and logging none of it — which is
// what an io.Discard costs — leaves no record of when anything changed. Keeping
// the distinct lines gives the timeline, because a row appears exactly when it
// first says what it says.
//
// It is not safe for concurrent use: one poll loop writes to it, which is what
// it is for.
func NewChangeWriter(emit func(line string)) io.Writer {
	return &changeWriter{emit: emit, seen: map[string]bool{}}
}

func (w *changeWriter) Write(p []byte) (int, error) {
	w.partial.Write(p)

	for {
		line, err := w.partial.ReadString('\n')
		if err != nil {
			// Not a whole line yet: put back what was consumed and wait for
			// the rest of it. A table's row can arrive in pieces.
			w.partial.WriteString(line)
			break
		}
		w.record(line)
	}
	return len(p), nil
}

func (w *changeWriter) record(line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" || w.seen[line] {
		return
	}
	w.seen[line] = true
	w.emit(line)
}
