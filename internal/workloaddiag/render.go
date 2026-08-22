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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// Render writes the reports as one document: the tables first, so that the
// shape of the failure is readable at a glance, and the logs after them,
// because they are long and are read second.
//
// Nothing is written for no reports. A caller that collected nothing has
// nothing to say, and an empty heading in a test log reads as a bug in the
// harness rather than as an absence of findings.
func Render(w io.Writer, reports []Report) error {
	if len(reports) == 0 {
		return nil
	}

	for _, report := range reports {
		if err := renderReport(w, report); err != nil {
			return err
		}
	}
	return nil
}

// Write renders the reports under dir as <name>.md, titled, and returns the
// path it wrote.
//
// A report kept only in a test log is not collectable: CI prints that log and
// nobody keeps it, and it is interleaved with everything else the run said.
func Write(dir, name, title string, reports []Report) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating the diagnostics directory %s: %w", dir, err)
	}

	var sb strings.Builder
	if _, err := fmt.Fprintf(&sb, "# %s\n", title); err != nil {
		return "", err
	}
	if err := Render(&sb, reports); err != nil {
		return "", err
	}

	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

func renderReport(w io.Writer, report Report) error {
	title := report.Workspace
	if report.Cluster != "" {
		title = fmt.Sprintf("%s (%s)", title, report.Cluster)
	}
	if _, err := fmt.Fprintf(w, "\n## %s\n", strings.TrimSpace(title)); err != nil {
		return err
	}

	if err := renderNodes(w, report.Nodes); err != nil {
		return err
	}
	if err := renderDaemonSets(w, report.DaemonSets); err != nil {
		return err
	}
	if err := renderPods(w, report.Pods); err != nil {
		return err
	}
	if err := renderProbes(w, report.Probes); err != nil {
		return err
	}
	if err := renderLogs(w, report.Pods); err != nil {
		return err
	}
	return renderNotes(w, report.Notes)
}

// fenced writes a section's body inside a code fence. The report is a .md
// file, and a fixed-width table is a paragraph of collapsed whitespace to
// anything that renders markdown — which is what a reader opening the CI
// artifact is using.
func fenced(w io.Writer, body func(io.Writer) error) error {
	if _, err := fmt.Fprintln(w, "\n```"); err != nil {
		return err
	}
	if err := body(w); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "```")
	return err
}

func renderNodes(w io.Writer, nodes []Node) error {
	if len(nodes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### Nodes"); err != nil {
		return err
	}

	return fenced(w, func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "NODE\tREADY\tREASON\tMESSAGE"); err != nil {
			return err
		}
		for _, n := range nodes {
			ready := n.Ready()
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				n.Name, orDash(ready.Status), orDash(ready.Reason), orDash(ready.Message)); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}

		for _, n := range nodes {
			others := n.Others()
			if len(others) == 0 {
				continue
			}
			compact := make([]string, 0, len(others))
			for _, c := range others {
				compact = append(compact, fmt.Sprintf("%s=%s", c.Type, c.Status))
			}
			if _, err := fmt.Fprintf(w, "\n%s other conditions: %s\n", n.Name, strings.Join(compact, ", ")); err != nil {
				return err
			}
		}
		return nil
	})
}

func renderDaemonSets(w io.Writer, daemonSets []DaemonSet) error {
	if len(daemonSets) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### DaemonSets"); err != nil {
		return err
	}

	return fenced(w, func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "NAMESPACE\tNAME\tDESIRED\tCURRENT\tREADY\tAVAILABLE"); err != nil {
			return err
		}
		for _, ds := range daemonSets {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\n",
				ds.Namespace, ds.Name, ds.Desired, ds.Current, ds.Ready, ds.Available); err != nil {
				return err
			}
		}
		return tw.Flush()
	})
}

func renderPods(w io.Writer, pods []Pod) error {
	if len(pods) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### Pods"); err != nil {
		return err
	}

	return fenced(w, func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "NAMESPACE\tNAME\tNODE\tPHASE\tREADY\tRESTARTS\tDETAIL"); err != nil {
			return err
		}
		for _, p := range pods {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				p.Namespace, p.Name, orDash(p.Node), orDash(p.Phase), p.Ready, p.Restarts, orDash(p.Detail)); err != nil {
				return err
			}
		}
		return tw.Flush()
	})
}

func renderProbes(w io.Writer, probes []Probe) error {
	if len(probes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### On the node"); err != nil {
		return err
	}
	for _, p := range probes {
		// Output *and* error: a command that failed part way through still
		// said something, and what it said is the finding as often as the
		// error is.
		body := strings.TrimRight(p.Output, "\n")
		if p.Err != "" {
			if body != "" {
				body += "\n"
			}
			body += "error: " + p.Err
		}
		if strings.TrimSpace(body) == "" {
			body = "(no output)"
		}
		if _, err := fmt.Fprintf(w, "\n#### %s\n\n```\n%s\n```\n", p.Description, body); err != nil {
			return err
		}
	}
	return nil
}

func renderLogs(w io.Writer, pods []Pod) error {
	var any bool
	for _, p := range pods {
		if len(p.Logs) > 0 {
			any = true
			break
		}
	}
	if !any {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### Logs"); err != nil {
		return err
	}

	for _, p := range pods {
		for _, l := range p.Logs {
			title := fmt.Sprintf("%s/%s %s", p.Namespace, p.Name, l.Container)
			if l.Previous {
				title += " (previous container)"
			}
			body := l.Content
			if l.Err != "" {
				body = l.Err
			}
			if strings.TrimSpace(body) == "" {
				body = "(empty)"
			}
			if _, err := fmt.Fprintf(w, "\n#### %s\n\n```\n%s\n```\n", title, strings.TrimRight(body, "\n")); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderNotes(w io.Writer, notes []string) error {
	if len(notes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### Not read"); err != nil {
		return err
	}
	for _, n := range notes {
		if _, err := fmt.Fprintf(w, "- %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
