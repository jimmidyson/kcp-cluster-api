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

package deployedscale

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ComponentSample is one deployment at one moment: what its process says about
// itself, and what the cluster says about its container.
type ComponentSample struct {
	Component string        `json:"component"`
	Process   ProcessSample `json:"process"`
	Pod       PodFacts      `json:"pod"`
}

// Sample is the whole fleet at one moment.
type Sample struct {
	Label      string            `json:"label"`
	Workspaces int               `json:"workspaces"`
	Clusters   int               `json:"clusters"`
	Nodes      int               `json:"nodes"`
	Taken      time.Time         `json:"taken"`
	Components []ComponentSample `json:"components"`
}

// Component returns one component's part of the sample.
func (s Sample) Component(name string) (ComponentSample, bool) {
	for _, c := range s.Components {
		if c.Component == name {
			return c, true
		}
	}
	return ComponentSample{}, false
}

// Report is what a deployed run produces.
type Report struct {
	Title string            `json:"title"`
	Facts map[string]string `json:"facts,omitempty"`
	// Samples in the order they were taken.
	Samples []Sample `json:"samples"`
	// Reconciliations record how this run compares with an in-process run of
	// the same shape. Empty when none was asked for.
	Reconciliations []Reconciliation `json:"reconciliations,omitempty"`
}

// AddFact records one condition the numbers only mean anything under.
func (r *Report) AddFact(key, value string) {
	if r.Facts == nil {
		r.Facts = map[string]string{}
	}
	r.Facts[key] = value
}

// Add appends a sample.
func (r *Report) Add(s Sample) {
	if s.Taken.IsZero() {
		s.Taken = time.Now()
	}
	r.Samples = append(r.Samples, s)
}

// Placement describes where the run's components ran.
//
// It is a fact rather than a footnote: a run in which everything landed on one
// node measured a co-located deployment, whatever the manifests asked for, and
// reporting it as a multi-node figure would be the single most misleading
// thing this harness could do.
func (r *Report) Placement() (nodes []string, coLocated bool) {
	seen := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			if c.Pod.Node != "" {
				seen[c.Pod.Node] = true
			}
		}
	}
	for n := range seen {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	components := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			components[c.Component] = true
		}
	}
	// Co-located when more than one component shared the single node they all
	// ran on. One component cannot be spread, so it is not co-located either.
	return nodes, len(nodes) == 1 && len(components) > 1
}

// Disturbed reports components whose containers restarted during the run.
//
// A restart resets every process metric, so a run containing one has samples
// that are not comparable with each other however reasonable they look. This
// is reported rather than corrected: there is no honest correction.
func (r *Report) Disturbed() []ComponentSample {
	var out []ComponentSample
	for _, s := range r.Samples {
		for _, c := range s.Components {
			if !c.Pod.Comparable() {
				out = append(out, c)
			}
		}
	}
	return out
}

// Fit is what a run can say about the cost of one more unit of fleet.
type Fit struct {
	// Slope is the marginal cost of one more workspace, or of one more
	// cluster, depending on which fit was asked for.
	Slope float64
	// Base is the fitted cost at zero units of fleet. It is not what the
	// process costs idle — see [Report.Idle], and see [Report.fit] for why an
	// idle sample is not one of the points.
	Base float64
	// OK is false when the run cannot support a slope at all, and then Why
	// says what is missing. A report prints Why where the figure would go.
	OK  bool
	Why string
}

// PerWorkspace fits a least-squares slope of one measure against the workspace
// count, for one component.
//
// A negative slope is refused for the reason the in-process target run refuses
// it: least squares returns one whenever the noise in a quantity exceeds its
// signal across the swept range, and a negative cost per workspace is not a
// cheaper fleet.
func (r *Report) PerWorkspace(component string, measure func(ComponentSample) float64) (float64, bool) {
	f := r.FitPerWorkspace(component, measure)
	return f.Slope, f.OK
}

// PerCluster is the same fit against cluster count, and on the evidence so far
// it is the one that predicts cost.
//
// # Why both are reported
//
// Twenty-five clusters were measured twice, as twenty-five workspaces of one
// and as five workspaces of five. Per workspace the two disagree wildly —
// core-manager at 17.0 against 77.0 — and a reader comparing those two reports
// would conclude that packing clusters into fewer workspaces is four times
// more expensive. Per cluster they agree: 17.0 against 15.4, and the control
// plane manager at 46.0 against 46.1.
//
// So the workspace grouping is close to free and the cluster count is what
// costs, which is exactly the question a fleet target asks: whether 200
// clusters in 200 workspaces differs from 200 in 20. Reporting only the
// per-workspace figure hides that behind an artefact of how the fleet was
// arranged.
func (r *Report) PerCluster(component string, measure func(ComponentSample) float64) (float64, bool) {
	f := r.FitPerCluster(component, measure)
	return f.Slope, f.OK
}

// FitPerWorkspace and FitPerCluster are the same two fits with the reason a
// refused one gives, which is what a report prints in its place.
func (r *Report) FitPerWorkspace(component string, measure func(ComponentSample) float64) Fit {
	return r.fit(component, measure, func(s Sample) float64 { return float64(s.Workspaces) })
}

func (r *Report) FitPerCluster(component string, measure func(ComponentSample) float64) Fit {
	return r.fit(component, measure, func(s Sample) float64 { return float64(s.Clusters) })
}

// Idle is what a component cost before the run created anything: the sample
// taken with no workspaces and no clusters, if the run took one.
//
// It is reported beside the fits rather than inside them. See [Report.fit].
func (r *Report) Idle(component string) (ComponentSample, bool) {
	for _, s := range r.Samples {
		if s.Workspaces != 0 || s.Clusters != 0 {
			continue
		}
		if c, ok := s.Component(component); ok && c.Pod.Comparable() {
			return c, true
		}
	}
	return ComponentSample{}, false
}

func (r *Report) fit(component string, measure func(ComponentSample) float64, x func(Sample) float64) Fit {
	var xs, ys []float64
	for _, s := range r.Samples {
		c, ok := s.Component(component)
		if !ok || !c.Pod.Comparable() {
			continue
		}
		// An idle process is not a small fleet.
		//
		// The 50x10 run sampled kcp before it had a workspace to serve: 502 MB
		// of live heap, against 1.41 GB thirteen workspaces later and 1.92 GB
		// at fifty. The loaded points lie on a line to within 1.4%; adding the
		// idle one nearly doubles the slope, to 25.5 MiB per cluster, and still
		// misses the idle point by 290 MB. Nothing about the fleet changed
		// between those two answers.
		//
		// What changed is the regime. An idle apiserver has not built the
		// caches, watches or decoded schemas that the first bound workspace
		// makes it build, so the step from nothing to something is not the
		// first stride of the line that follows. Fitting across it measures the
		// step, and then attributes it to whichever unit is on the x axis.
		//
		// So the fit is over the loaded samples, and the idle sample is
		// reported on its own, next to the fit's own base. The gap between them
		// is a real quantity — for kcp, 735 MB — and it belongs in front of a
		// reader rather than smeared across a per-cluster figure.
		if x(s) == 0 {
			continue
		}
		xs = append(xs, x(s))
		ys = append(ys, measure(c))
	}
	// Three distinct sample points, not two.
	//
	// A two-point fit passes exactly through both points. Its residual is
	// identically zero whatever the data, so it offers no way at all to tell a
	// slope from the difference between two noisy samples — and goroutine counts
	// are noisy: in-flight requests, a watch reconnecting, a worker pool that
	// happens to be busy. A run at 1 and 2 workspaces reported 17.0 goroutines
	// per workspace from 416 and 433, a 4% swing on a 400-goroutine process,
	// and that number then disagreed 8.5x with a 61-sample in-process sweep. The
	// disagreement was about the fit, not about the fleet.
	//
	// So a run too small to resolve a slope reports no slope. Publishing one it
	// cannot support is the thing this repository has already decided is worse
	// than publishing nothing.
	if distinct(xs) < 3 {
		return Fit{Why: notMeasured}
	}

	return fitPoints(xs, ys)
}

// SplitByNodes separates what a cluster costs from what a Machine in it costs,
// across runs that differ in nodes per cluster.
//
// # Why this cannot be done inside one run
//
// A run gives every cluster the same number of nodes, so its cluster count and
// its Machine count rise together and no fit through its samples can tell the
// two apart. What separates them is the same fleet measured at more than one
// node count: each run's cost per cluster is one point, its nodes per cluster
// is the x, and the slope through those points is the cost of a Machine.
//
// Three node counts, not two, for the reason every other fit here needs three:
// the two-point split of the first two collected runs read 1.38 MB per Machine
// and the three-point fit reads 1.47.
//
// Every run in the set must have been sampled the same way. Live heap read
// without forcing a collection first carries the collector's timing, and three
// runs that did that disagreed by a factor of four — see
// [CollectGarbage]. This function cannot check that, because it is a property
// of how the samples were taken rather than of the samples; the caller checks
// the kcpHeapSample fact.
func SplitByNodes(reports []*Report, component string, measure func(ComponentSample) float64) Fit {
	var xs, ys []float64
	for _, r := range reports {
		nodes, ok := r.NodesPerCluster()
		if !ok {
			continue
		}
		perCluster := r.FitPerCluster(component, measure)
		if !perCluster.OK {
			return Fit{Why: "not measured (one of the runs could not price a cluster: " + perCluster.Why + ")"}
		}
		xs = append(xs, float64(nodes))
		ys = append(ys, perCluster.Slope)
	}
	if distinct(xs) < 3 {
		return Fit{Why: notMeasuredNodeCounts}
	}
	return fitPoints(xs, ys)
}

// NodesPerCluster is how many nodes each cluster in this run was given, and
// whether every loaded sample agrees. A run whose samples disagree is not one
// point on a line against node count.
func (r *Report) NodesPerCluster() (int, bool) {
	nodes := 0
	for _, s := range r.Samples {
		if s.Clusters == 0 || s.Nodes == 0 {
			continue
		}
		per := s.Nodes / s.Clusters
		if per*s.Clusters != s.Nodes {
			return 0, false
		}
		if nodes == 0 {
			nodes = per
		} else if nodes != per {
			return 0, false
		}
	}
	return nodes, nodes > 0
}

// fitPoints is the least squares, the sign check and the residual gate that
// every figure in a report goes through. It takes points rather than samples so
// that a fit across runs is held to the same standard as a fit within one.
func fitPoints(xs, ys []float64) Fit {
	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(xs))
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumXX += xs[i] * xs[i]
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return Fit{Why: notMeasured}
	}
	slope := (n*sumXY - sumX*sumY) / denominator
	if slope < 0 {
		return Fit{Why: notMeasuredNegative}
	}
	base := (sumY - slope*sumX) / n

	// Having three points is not the same as their lying on a line.
	//
	// Least squares answers whatever it is asked. The 50x10 run's resident
	// series for kcp climbs monotonically, has three well-spaced points and a
	// respectable R-squared of 0.9, and still misses its own line by 7% of the
	// range it spans, because resident memory carries GOGC's headroom as well
	// as the fleet. Its live heap over the same three samples misses by 1.4%.
	// The two used to be printed side by side as though they were the same kind
	// of number.
	//
	// The threshold is a judgement, and this is the reasoning behind it: the
	// per-cluster goroutine figure reproduces across fleet distributions to
	// about 1.6%, and the memory figures that disagreed between distributions
	// by 29-78% came from series whose residuals were well above 5%. A fit
	// looser than that has not earned the word "measured".
	if worst, spread := worstResidual(xs, ys, slope, base); spread > 0 && worst > maxRelativeResidual*spread {
		return Fit{Why: fmt.Sprintf(notALine, 100*worst/spread, 100*maxRelativeResidual)}
	}
	return Fit{Slope: slope, Base: base, OK: true}
}

// worstResidual returns how far the furthest point lies from the fitted line,
// and the range the points span, which is what that distance is judged against.
func worstResidual(xs, ys []float64, slope, base float64) (worst, spread float64) {
	lo, hi := ys[0], ys[0]
	for i := range xs {
		d := ys[i] - (base + slope*xs[i])
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
		lo = min(lo, ys[i])
		hi = max(hi, ys[i])
	}
	return worst, hi - lo
}

// Goroutines and Resident are the measures PerWorkspace is usually asked for.
func Goroutines(c ComponentSample) float64 { return float64(c.Process.Goroutines) }
func Resident(c ComponentSample) float64   { return float64(c.Process.ResidentBytes) }
func HeapAlloc(c ComponentSample) float64  { return float64(c.Process.HeapAllocBytes) }

// Components lists every component that appears in the report, in the
// canonical order.
func (r *Report) Components() []string {
	seen := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			seen[c.Component] = true
		}
	}
	var out []string
	for _, c := range Components() {
		if seen[c.Name] {
			out = append(out, c.Name)
			delete(seen, c.Name)
		}
	}
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// Write renders the report to dir as name.md and name.json, as the sweeps do.
func (r *Report) Write(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(r.Markdown()), 0o600); err != nil {
		return fmt.Errorf("writing the markdown report: %w", err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the json report: %w", err)
	}
	return nil
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", r.Title)

	nodes, coLocated := r.Placement()
	fmt.Fprintf(&b, "| Condition | Value |\n|---|---|\n")
	keys := make([]string, 0, len(r.Facts))
	for k := range r.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %s |\n", k, r.Facts[k])
	}
	fmt.Fprintf(&b, "| nodes | %s |\n", strings.Join(nodes, ", "))
	// Stated in the report itself rather than left for a reader to work out
	// from the node list.
	if coLocated {
		fmt.Fprintf(&b, "| placement | **co-located: every component ran on one node, so this is not a multi-node figure** |\n")
	} else if len(nodes) > 1 {
		fmt.Fprintf(&b, "| placement | spread across %d nodes |\n", len(nodes))
	}
	b.WriteString("\n")

	if disturbed := r.Disturbed(); len(disturbed) > 0 {
		b.WriteString("> **A container restarted during this run.** Every process metric resets when the\n")
		b.WriteString("> process does, so the samples below are not comparable with each other:\n>\n")
		seen := map[string]bool{}
		for _, c := range disturbed {
			if seen[c.Component] {
				continue
			}
			seen[c.Component] = true
			reason := c.Pod.LastReason
			if reason == "" {
				reason = "unknown reason"
			}
			fmt.Fprintf(&b, "> - `%s` restarted %d time(s), last: %s\n", c.Component, c.Pod.RestartCount, reason)
		}
		b.WriteString("\n")
	}

	for _, component := range r.Components() {
		fmt.Fprintf(&b, "## %s\n\n", component)
		b.WriteString("| Step | Workspaces | Clusters | Goroutines | Heap | Resident | RSS/heap | CPU | Node |\n")
		b.WriteString("|---|--:|--:|--:|--:|--:|--:|--:|---|\n")
		for _, s := range r.Samples {
			c, ok := s.Component(component)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %.2fx | %.1fs | %s |\n",
				s.Label, s.Workspaces, s.Clusters,
				c.Process.Goroutines, humanBytes(c.Process.HeapAllocBytes), humanBytes(c.Process.ResidentBytes),
				c.Process.ResidentToHeapRatio(), c.Process.CPUSeconds, c.Pod.Node)
		}
		b.WriteString("\n")

		// What the process cost before the run created anything, stated
		// before the fits so that a reader meets the measured fixed cost
		// before meeting the fitted one. See Report.fit.
		if idle, ok := r.Idle(component); ok {
			fmt.Fprintf(&b, "- measured idle, before any workspace existed: %d goroutines, %s resident, %s heap\n",
				idle.Process.Goroutines, humanBytes(idle.Process.ResidentBytes), humanBytes(idle.Process.HeapAllocBytes))
		}

		countPer := func(label string, f Fit) {
			if f.OK {
				fmt.Fprintf(&b, "- %s: **%.1f**\n", label, f.Slope)
				return
			}
			fmt.Fprintf(&b, "- %s: %s\n", label, f.Why)
		}
		bytesPer := func(label string, f Fit) {
			if f.OK {
				fmt.Fprintf(&b, "- %s: **%s**\n", label, humanBytes(uint64(f.Slope)))
				return
			}
			fmt.Fprintf(&b, "- %s: %s\n", label, f.Why)
		}

		countPer("goroutines per workspace", r.FitPerWorkspace(component, Goroutines))
		bytesPer("resident bytes per workspace", r.FitPerWorkspace(component, Resident))
		// Live heap as well as resident. The two answer different questions: a
		// container limit is set against resident memory, but resident carries
		// the collector's headroom, and on the evidence so far it is the heap
		// series that lies on a line. Reporting only resident is how a figure
		// fitted to GOGC gets published as a cost per cluster.
		bytesPer("heap bytes per workspace", r.FitPerWorkspace(component, HeapAlloc))
		// Per cluster as well as per workspace. See PerCluster: the two agree
		// only when a workspace holds one cluster, and it is the per-cluster
		// figure that has held across every distribution measured so far.
		if f := r.FitPerCluster(component, Goroutines); f.OK {
			countPer("goroutines per cluster", f)
		}
		if f := r.FitPerCluster(component, Resident); f.OK {
			bytesPer("resident bytes per cluster", f)
		}
		if f := r.FitPerCluster(component, HeapAlloc); f.OK {
			bytesPer("heap bytes per cluster", f)
			// The step between the two fixed costs, which is a measured
			// quantity in its own right and the largest single number in the
			// kcp column of a small run.
			if idle, ok := r.Idle(component); ok && f.Base > float64(idle.Process.HeapAllocBytes) {
				fmt.Fprintf(&b, "  - the fit's own fixed cost is %s, which is %s above the idle process. "+
					"The run does not resolve that step into what the first workspaces made the process "+
					"build and what it was holding in flight while it built it.\n",
					humanBytes(uint64(f.Base)), humanBytes(uint64(f.Base)-idle.Process.HeapAllocBytes))
			}
		}
		if !monotonic(r, component, HeapAlloc) {
			b.WriteString("- " + heapWobble + "\n")
		}
		b.WriteString("\n")
	}

	if len(r.Reconciliations) > 0 {
		b.WriteString("## Reconciliation with the in-process instrument\n\n")
		b.WriteString("| Quantity | Deployed | In process | Ratio | Within tolerance |\n|---|--:|--:|--:|---|\n")
		for _, rec := range r.Reconciliations {
			verdict := yesNo(rec.WithinTolerance)
			if !rec.Comparable {
				verdict = "**not a like-for-like comparison**"
			}
			fmt.Fprintf(&b, "| %s | %.1f | %.1f | %.2fx | %s |\n",
				rec.Quantity, rec.Deployed, rec.InProcess, rec.Ratio, verdict)
		}
		b.WriteString("\n")
		for _, rec := range r.Reconciliations {
			if !rec.Comparable && rec.Why != "" {
				b.WriteString("- " + rec.Why + "\n")
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "**no**"
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// SortedNodes is the set of nodes a sample's components ran on.
func (s Sample) SortedNodes() []string {
	seen := map[string]bool{}
	for _, c := range s.Components {
		if c.Pod.Node != "" {
			seen[c.Pod.Node] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// notMeasured is what a report says instead of a per-workspace figure it cannot
// support. See PerWorkspace for why three points rather than two.
const notMeasured = "not measured (a slope needs at least three distinct workspace counts; " +
	"a fit through two points has no residual and so cannot be told from noise)"

// notMeasuredNegative is what a report says when least squares came back with a
// negative cost per unit of fleet, which is noise exceeding signal rather than
// a cheaper fleet.
const notMeasuredNegative = "not measured (the fit came back negative, which is noise exceeding " +
	"signal across the swept range rather than a fleet that costs less the larger it gets)"

// notALine is what a report says when the points it has do not describe one.
// See [Report.fit] for where the threshold comes from.
const notALine = "not measured (the samples do not lie on a line: the furthest is %.0f%% of their own " +
	"range away from it, against the %.0f%% this harness will call a measurement)"

// notMeasuredNodeCounts is what a split says when the runs it was given do not
// span enough node counts to separate a cluster's cost from a Machine's.
const notMeasuredNodeCounts = "not measured (separating a cluster's cost from a Machine's needs runs " +
	"at three distinct nodes-per-cluster counts; two of them make a line through both whatever the " +
	"data says, and the two-point split of the first two collected runs was 7% from the three-point one)"

// maxRelativeResidual is how far the furthest sample may lie from the fitted
// line, as a fraction of the range the samples span, before the fit stops being
// reported as a measurement.
const maxRelativeResidual = 0.05

// distinct counts how many different values appear in xs.
func distinct(xs []float64) int {
	seen := make(map[float64]struct{}, len(xs))
	for _, x := range xs {
		seen[x] = struct{}{}
	}
	return len(seen)
}

// heapWobble is the caveat on the memory figures, printed when a run's own heap
// series says they cannot be trusted as a cost per unit of fleet.
//
// # Why the memory slopes get a caveat the goroutine ones do not
//
// Goroutine counts rise monotonically with fleet size in every run measured so
// far, and their slopes reproduce: twenty-five clusters arranged as 25x1 and as
// 5x5 agree to 1.6% in total, and the control plane manager to 0.3%.
//
// The memory slopes do not. The same two runs disagree by 29%, 15%, 78% and 76%
// per component. The reason is in each run's own heap column: it does not climb
// with the fleet, it wanders — the dev infrastructure manager sampled 26.3, 19.0
// and 44.5 MiB in one sweep and 20.4, 27.3 and 86.9 MiB in the other. A sample
// is taken whenever a checkpoint is reached, which is whenever it is reached
// relative to a garbage collection, and a line fitted through that measures when
// the collector last ran as much as it measures the fleet.
//
// The figures stay: resident memory is what a limit is set against, and a wide
// figure is more use than none. What they do not get is the same standing as a
// count that reproduces.
const heapWobble = "**The memory figures above are weaker than the goroutine ones.** This run's heap did not " +
	"climb monotonically with the fleet (see the Heap column), so a slope through it is fitted partly to " +
	"garbage collection timing. Per-cluster memory has not reproduced across fleet distributions; " +
	"per-cluster goroutines has."

// monotonic reports whether a component's measure is non-decreasing across the
// run's comparable samples, which is the cheapest available signal that a slope
// through it describes the fleet rather than the runtime's timing.
func monotonic(r *Report, component string, measure func(ComponentSample) float64) bool {
	var last float64
	first := true
	for _, s := range r.Samples {
		c, ok := s.Component(component)
		if !ok || !c.Pod.Comparable() {
			continue
		}
		v := measure(c)
		if !first && v < last {
			return false
		}
		last, first = v, false
	}
	return true
}
