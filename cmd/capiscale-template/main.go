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

// Command capiscale-template turns the manifest clusterctl generated from
// CAREN's quick-start example into a management cluster for a scale test:
// a fixed worker count instead of an autoscaled one, and without the addons
// this measurement does not use.
//
// Reads a generated manifest on stdin, writes one on stdout. It runs after
// clusterctl rather than on the template because the template's ${VARIABLE}
// placeholders sit in fields that are numbers once substituted, and a round
// trip through a YAML parser would quote them.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

func main() {
	fs := flag.NewFlagSet("capiscale-template", flag.ExitOnError)
	workers := fs.Int("workers", 4, "Replicas for every worker pool, replacing the autoscaler "+
		"annotations CAREN's example sizes them with. A scale test cannot have its own management "+
		"cluster resizing underneath it.")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading stdin: %v\n", err)
		os.Exit(1)
	}
	out, err := upstreamscale.TrimForScale(string(in), *workers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not trim the manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
