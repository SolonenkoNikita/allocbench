package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/cell"
	"github.com/SolonenkoNikita/allocbench/internal/doctor"
	"github.com/SolonenkoNikita/allocbench/internal/plan"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/results"
)

const (
	allNames     = "all"
	runnerRecipe = "runner"
	runIDFormat  = "20060102T150405Z"
	cellsDir     = "cells"
	bytesPerKB   = 1024
	hostRoot     = "/"
)

type runOptions struct {
	plan    string
	allocs  []string
	benches []string
	runEnv  string
	repeat  int
	order   string
	cpus    string
	results string
	jobs    int
}

func newRunCommand(opts *options) *cobra.Command {
	ro := &runOptions{jobs: runtime.NumCPU()}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Build everything a plan needs and run its cells",
		Long: "Run every bench under every allocator in a fresh sandbox and measure it.\n" +
			"A plan comes from --plan, from flags, or both: flags override the plan file.\n" +
			"Use all to take every allocator or bench.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			p, err := runPlan(cmd, ro, s)
			if err != nil {
				return err
			}
			resolved, err := plan.Resolve(p, s)
			if err != nil {
				return fmt.Errorf("invalid plan:\n%w", err)
			}
			return execute(cmd, opts, ro, s, p, resolved)
		},
	}
	f := cmd.Flags()
	f.StringVar(&ro.plan, "plan", "", "plan file")
	f.StringSliceVar(&ro.allocs, "alloc", nil, "allocators, comma separated, or all")
	f.StringSliceVar(&ro.benches, "bench", nil, "benches, comma separated, or all")
	f.StringVar(&ro.runEnv, "run-env", "", "environment to run benches in (default: the only env recipe)")
	f.IntVar(&ro.repeat, "repeat", 1, "how many times to run every cell")
	f.StringVar(&ro.order, "order", plan.OrderInterleaved, "interleaved or sequential")
	f.StringVar(&ro.cpus, "cpus", "", "pin benches to these CPUs, a list like 2-5,7")
	f.StringVar(&ro.results, "results", "results", "directory for results")
	f.IntVarP(&ro.jobs, "jobs", "j", ro.jobs, "parallel jobs inside a build ($JOBS)")
	return cmd
}

func runPlan(cmd *cobra.Command, ro *runOptions, s *recipe.Set) (*plan.Plan, error) {
	p := plan.New()
	if ro.plan != "" {
		loaded, err := plan.Load(ro.plan)
		if err != nil {
			return nil, err
		}
		p = loaded
	}
	flags := cmd.Flags()
	if flags.Changed("alloc") {
		p.Allocators = expandAll(ro.allocs, keys(s.Allocators))
	}
	if flags.Changed("bench") {
		p.Benches = expandAll(ro.benches, keys(s.Benches))
	}
	if flags.Changed("run-env") {
		p.RunEnv = ro.runEnv
	}
	if flags.Changed("repeat") {
		p.Repeat = ro.repeat
	}
	if flags.Changed("order") {
		p.Order = ro.order
	}
	if flags.Changed("cpus") {
		p.CPUs = ro.cpus
	}

	if len(p.Allocators) == 0 {
		return nil, fmt.Errorf("choose allocators with --alloc: %s or all", strings.Join(keys(s.Allocators), ", "))
	}
	if len(p.Benches) == 0 {
		return nil, fmt.Errorf("choose benches with --bench: %s or all", strings.Join(keys(s.Benches), ", "))
	}
	if p.RunEnv == "" && len(s.Envs) == 1 {
		p.RunEnv = keys(s.Envs)[0]
	}
	if p.RunEnv == "" {
		return nil, fmt.Errorf("choose an environment with --run-env: %s", strings.Join(keys(s.Envs), ", "))
	}
	return p, nil
}

func expandAll(names, all []string) []string {
	if len(names) == 1 && names[0] == allNames {
		return all
	}
	return names
}

func execute(cmd *cobra.Command, opts *options, ro *runOptions, s *recipe.Set, p *plan.Plan, resolved *plan.Resolved) error {
	if _, ok := s.Tools[runnerRecipe]; !ok {
		return fmt.Errorf("tools/%s: no such recipe, it builds the runner", runnerRecipe)
	}
	host := results.ReadHost(hostRoot)
	cpus, err := pinnedCPUs(p.CPUs, host.AllowedCPUs)
	if err != nil {
		return err
	}
	b, err := newBuilder(opts, s, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	b.Jobs = ro.jobs

	dirs := map[string]string{}
	hashes := map[string]string{}
	refs := []string{"tools/" + runnerRecipe}
	for _, a := range p.Allocators {
		refs = append(refs, "allocators/"+a)
	}
	for _, bn := range p.Benches {
		refs = append(refs, "benches/"+bn)
	}
	for _, ref := range refs {
		r, err := b.Build(ref)
		if err != nil {
			return err
		}
		dirs[ref] = r.Dir
		hashes[ref] = r.Hash
	}
	runEnv, err := b.Env(p.RunEnv)
	if err != nil {
		return err
	}

	started := time.Now()
	dir, err := resultsDir(ro.results, started)
	if err != nil {
		return err
	}
	envRecipe := s.Envs[p.RunEnv]
	manifest := results.Manifest{
		Version: opts.version,
		Started: started.UTC(),
		Plan: results.Plan{
			Allocators: p.Allocators,
			Benches:    p.Benches,
			Repeat:     p.Repeat,
			Order:      p.Order,
			CPUs:       plan.FormatCPUs(cpus),
			Cells:      len(resolved.Cells),
		},
		RunEnv: results.Env{
			Name:     p.RunEnv,
			Distro:   envRecipe.Distro,
			Suite:    envRecipe.Suite,
			Snapshot: envRecipe.Snapshot,
			Packages: envRecipe.Packages,
			Hash:     runEnv.Hash,
			Tree:     runEnv.Tree,
			Mirror:   runEnv.Mirror,
		},
		Builds:   hashes,
		Host:     host,
		Warnings: []string{},
	}
	warnings := runWarnings(hostConditions(), resolved.Cells, cpus, host.CPUs)
	for _, f := range warnings {
		manifest.Warnings = append(manifest.Warnings, f.Check+": "+f.Detail)
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n  fix: %s\n", f.Check, f.Detail, f.Fix)
	}
	if err := results.WriteManifest(dir, manifest); err != nil {
		return err
	}
	w, err := results.Create(dir)
	if err != nil {
		return err
	}
	defer w.Close()

	ex := &cell.Executor{Rootfs: runEnv.Rootfs, Runner: dirs["tools/"+runnerRecipe], CPUs: manifest.Plan.CPUs}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "host: %s\n", manifest.Host)
	if len(cpus) > 0 {
		fmt.Fprintf(out, "pinned to CPUs %s\n", manifest.Plan.CPUs)
	}
	fmt.Fprintf(out, "running %d cells, results in %s\n", len(resolved.Cells), dir)

	var done []cell.Result
	width := len(fmt.Sprint(len(resolved.Cells)))
	for i, c := range resolved.Cells {
		r, err := ex.Run(cell.Input{
			Cell:     c,
			Alloc:    s.Allocators[c.Alloc],
			AllocDir: dirs["allocators/"+c.Alloc],
			Bench:    s.Benches[c.Bench],
			BenchDir: dirs["benches/"+c.Bench],
			Out:      filepath.Join(dir, cellsDir, c.ID()),
		})
		if err != nil {
			return err
		}
		done = append(done, r)
		if err := w.Add(r); err != nil {
			return err
		}
		fmt.Fprintf(out, "[%*d/%d] %-28s %-12s %8s %8s\n", width, i+1, len(resolved.Cells), c.ID(), r.Status,
			formatDuration(r.Measure.Wall()), formatKB(r.Measure.MaxRSSKB))
	}

	finished := time.Now().UTC()
	manifest.Finished = &finished
	if err := results.WriteManifest(dir, manifest); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	fmt.Fprintln(out)
	summary, err := os.Create(filepath.Join(dir, results.SummaryFile))
	if err != nil {
		return err
	}
	defer summary.Close()
	if err := printSummary(io.MultiWriter(out, summary), p, done); err != nil {
		return err
	}
	if err := summary.Close(); err != nil {
		return err
	}
	failed := 0
	for _, r := range done {
		if !r.OK() {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d cells failed, see %s", failed, len(done), filepath.Join(dir, cellsDir))
	}
	return nil
}

var hostConditions = func() []doctor.Finding {
	return doctor.New().Conditions()
}

func pinnedCPUs(text, allowed string) ([]int, error) {
	cpus, err := plan.ParseCPUs(text)
	if err != nil || len(cpus) == 0 {
		return nil, err
	}
	permitted, err := plan.ParseCPUs(allowed)
	if err != nil || len(permitted) == 0 {
		return cpus, nil
	}
	var missing []int
	for _, cpu := range cpus {
		if !slices.Contains(permitted, cpu) {
			missing = append(missing, cpu)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("cpus %s are not available, this process may use %s", plan.FormatCPUs(missing), allowed)
	}
	return cpus, nil
}

func runWarnings(conditions []doctor.Finding, cells []plan.Cell, cpus []int, hostCPUs int) []doctor.Finding {
	var warnings []doctor.Finding
	for _, f := range conditions {
		if f.Level == doctor.Warning {
			warnings = append(warnings, f)
		}
	}
	available, kind := hostCPUs, "CPUs"
	if len(cpus) > 0 {
		available, kind = len(cpus), "pinned CPUs"
	} else {
		warnings = append(warnings, doctor.Finding{
			Level:  doctor.Warning,
			Check:  "cpus",
			Detail: "not pinned, the scheduler moves benches between CPUs",
			Fix:    "set cpus in the plan or pass --cpus",
		})
	}
	seen := map[plan.Cell]bool{}
	for _, c := range cells {
		key := plan.Cell{Bench: c.Bench, Threads: c.Threads}
		if c.Threads <= available || seen[key] {
			continue
		}
		seen[key] = true
		warnings = append(warnings, doctor.Finding{
			Level:  doctor.Warning,
			Check:  "threads",
			Detail: fmt.Sprintf("%s runs %d threads on %d %s, they wait for each other", c.Bench, c.Threads, available, kind),
			Fix:    "pin more CPUs or run fewer threads",
		})
	}
	return warnings
}

func resultsDir(base string, now time.Time) (string, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join(base, now.UTC().Format(runIDFormat))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

type group struct {
	bench   string
	threads int
	alloc   string
}

type summary struct {
	group
	total int
	walls []time.Duration
	rss   []int64
}

func summarize(p *plan.Plan, results []cell.Result) []*summary {
	index := map[group]*summary{}
	var out []*summary
	for _, r := range results {
		g := group{r.Cell.Bench, r.Cell.Threads, r.Cell.Alloc}
		sm, ok := index[g]
		if !ok {
			sm = &summary{group: g}
			index[g] = sm
			out = append(out, sm)
		}
		sm.total++
		if r.OK() {
			sm.walls = append(sm.walls, r.Measure.Wall())
			sm.rss = append(sm.rss, r.Measure.MaxRSSKB)
		}
	}

	allocOrder := map[string]int{}
	for i, a := range p.Allocators {
		allocOrder[a] = i
	}
	benchOrder := map[string]int{}
	for i, bn := range p.Benches {
		benchOrder[bn] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.bench != b.bench {
			return benchOrder[a.bench] < benchOrder[b.bench]
		}
		if a.threads != b.threads {
			return a.threads < b.threads
		}
		return allocOrder[a.alloc] < allocOrder[b.alloc]
	})
	return out
}

func printSummary(w io.Writer, p *plan.Plan, results []cell.Result) error {
	rows := summarize(p, results)
	base := map[group]time.Duration{}
	for _, sm := range rows {
		key := group{bench: sm.bench, threads: sm.threads}
		if _, ok := base[key]; !ok && len(sm.walls) > 0 {
			base[key] = median(sm.walls)
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCH\tTHREADS\tALLOC\tOK\tMEDIAN\tMIN\tMAX\tRELATIVE\tMAX RSS")
	for _, sm := range rows {
		ok := fmt.Sprintf("%d/%d", len(sm.walls), sm.total)
		if len(sm.walls) == 0 {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t-\t-\t-\t-\t-\n", sm.bench, sm.threads, sm.alloc, ok)
			continue
		}
		med := median(sm.walls)
		relative := "-"
		if b := base[group{bench: sm.bench, threads: sm.threads}]; b > 0 {
			relative = fmt.Sprintf("%.2f", float64(med)/float64(b))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", sm.bench, sm.threads, sm.alloc, ok,
			formatDuration(med), formatDuration(minOf(sm.walls)), formatDuration(maxOf(sm.walls)),
			relative, formatKB(medianInt(sm.rss)))
	}
	return tw.Flush()
}

func median(values []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func medianInt(values []int64) int64 {
	durations := make([]time.Duration, len(values))
	for i, v := range values {
		durations[i] = time.Duration(v)
	}
	return int64(median(durations))
}

func minOf(values []time.Duration) time.Duration {
	out := values[0]
	for _, v := range values[1:] {
		out = min(out, v)
	}
	return out
}

func maxOf(values []time.Duration) time.Duration {
	out := values[0]
	for _, v := range values[1:] {
		out = max(out, v)
	}
	return out
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.3fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return d.String()
}

func formatKB(kb int64) string {
	if kb >= bytesPerKB*bytesPerKB {
		return fmt.Sprintf("%.1f GB", float64(kb)/bytesPerKB/bytesPerKB)
	}
	if kb >= bytesPerKB {
		return fmt.Sprintf("%.1f MB", float64(kb)/bytesPerKB)
	}
	return fmt.Sprintf("%d KB", kb)
}
