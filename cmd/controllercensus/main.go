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

// Command controllercensus counts the controllers and watches a set of
// reconciler packages register, by parsing their SetupWithManager functions.
//
// # Why this exists
//
// Per-workspace cost is a function of exactly two counts — controllers and
// watches — so the census is not bookkeeping, it *is* the cost:
//
//	goroutines/workspace = 2 + 7×controllers + 1×workers×controllers + 2×watches
//
// The first version of that census was estimated at "roughly 4 controllers, 19
// watches" and was wrong in both terms; the real figures are 5 and 14-15, and
// the correction moved a published capacity figure by 2.8×. A number that
// load-bearing should be derived by a tool a reader can re-run, not by counting
// by hand.
//
// # Why an AST parse rather than grep
//
// The chained builder calls have no leading dot — the dot ends the previous
// line — so `grep '\.Watches('` silently misses every one of them. That is
// exactly how the first estimate went wrong, and it failed *quietly*, returning
// plausible small numbers rather than an error. Parsing the syntax tree cannot
// make that mistake.
//
// # A second use, if the additive strategy of ADR-0003 is adopted
//
// A workspace-aware setup added alongside an upstream one never conflicts on
// rebase — and therefore goes silently stale when upstream adds a watch. This
// tool is the mechanism for the guard that would catch it: run it over both
// setups and compare the sets.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// builderMethods are the calls that register something. `For` counts as a watch
// as well as naming the reconciled type: it is an informer registration like
// any other, and the per-watch cost applies to it identically.
var builderMethods = map[string]string{
	"For":              "informer",
	"Owns":             "informer",
	"Watches":          "informer",
	"WatchesMetadata":  "informer",
	"WatchesRawSource": "raw",
}

// setupFuncs are the entry points a controller is built in. Both names are
// recognised so that a workspace-aware variant added alongside the upstream one
// (ADR-0003) is counted rather than ignored.
var setupFuncs = map[string]bool{
	"SetupWithManager":             true,
	"SetupWithMulticlusterManager": true,
}

type census struct {
	Package  string
	Function string
	Informer int
	Raw      int
	Types    []string
}

func main() {
	roots := flag.String("roots", "",
		"Comma-separated directories to walk. Every package under them with a setup function "+
			"is counted. Several are accepted because a deployment's controllers can span "+
			"modules — the core reconcilers and the dev infrastructure provider do.")
	flag.Parse()

	if *roots == "" {
		fail(fmt.Errorf("-roots is required: point it at the reconciler packages of one deployment"))
	}

	var results []census
	for _, root := range strings.Split(*roots, ",") {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		found, err := walk(root)
		if err != nil {
			fail(err)
		}
		results = append(results, found...)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Package != results[j].Package {
			return results[i].Package < results[j].Package
		}
		return results[i].Function < results[j].Function
	})
	if len(results) == 0 {
		fail(fmt.Errorf("no setup functions found under %s", *roots))
	}

	report(results)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "controllercensus: %v\n", err)
	os.Exit(1)
}

func walk(root string) ([]census, error) {
	var out []census

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// A file that does not parse is skipped rather than fatal: the tool
			// is pointed at dependency source it does not own, and one
			// unparseable file should not deny the census of everything else.
			return nil
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !setupFuncs[fn.Name.Name] {
				continue
			}
			c := countIn(fn)
			c.Package = packageOf(root, path, file.Name.Name)
			c.Function = fn.Name.Name
			out = append(out, c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Function < out[j].Function
	})
	return out, nil
}

// packageOf labels a result by its directory relative to the root, which is
// more useful than the Go package name — several reconcilers are package
// `cluster` or `machineset` under different parents.
func packageOf(root, path, fallback string) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || rel == "." {
		return fallback
	}
	return rel
}

func countIn(fn *ast.FuncDecl) census {
	var c census
	seen := map[string]bool{}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		kind, ok := builderMethods[sel.Sel.Name]
		if !ok {
			return true
		}

		switch kind {
		case "informer":
			c.Informer++
			if t := typeArg(call); t != "" && !seen[t] {
				seen[t] = true
				c.Types = append(c.Types, t)
			}
		case "raw":
			c.Raw++
		}
		return true
	})

	sort.Strings(c.Types)
	return c
}

// typeArg pulls the watched type out of `For(&clusterv1.Cluster{})` so the
// census can report which types a deployment would cache — the figure that says
// what splitting deployments duplicates.
func typeArg(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	unary, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok {
		return ""
	}
	lit, ok := unary.X.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

func report(results []census) {
	fmt.Printf("%-40s %-30s %9s %5s\n", "package", "function", "informer", "raw")
	fmt.Println(strings.Repeat("-", 88))

	var controllers, informer, raw int
	types := map[string]int{}

	for _, c := range results {
		fmt.Printf("%-40s %-30s %9d %5d\n", c.Package, c.Function, c.Informer, c.Raw)
		controllers++
		informer += c.Informer
		raw += c.Raw
		for _, t := range c.Types {
			types[t]++
		}
	}

	fmt.Println(strings.Repeat("-", 88))
	fmt.Printf("%-40s %-30s %9d %5d\n", fmt.Sprintf("%d controllers", controllers), "", informer, raw)

	fmt.Println("\ntypes watched, and by how many controllers:")
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Slice(names, func(i, j int) bool {
		if types[names[i]] != types[names[j]] {
			return types[names[i]] > types[names[j]]
		}
		return names[i] < names[j]
	})
	for _, t := range names {
		fmt.Printf("  %-32s %d\n", t, types[t])
	}

	// The figure the whole capacity model turns on, restated here so a reader
	// of the census does not have to go and find the formula.
	fmt.Printf("\nR16: goroutines/workspace = 2 + 7×%d + 1×workers×%d + 2×%d\n",
		controllers, controllers, informer)
	for _, workers := range []int{2, 4} {
		fmt.Printf("  at %d workers per controller: %d goroutines per workspace\n",
			workers, 2+7*controllers+workers*controllers+2*informer)
	}
}
