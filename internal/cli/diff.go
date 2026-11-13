package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/results"
)

const (
	missingValue = "-"
	percent      = 100
)

var (
	skippedFields = []string{"started", "finished"}
	fullHash      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type runRecord struct {
	fields map[string]string
	lists  map[string][]string
	times  map[group][]time.Duration
	groups []group
}

func newDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <run> <run>",
		Short: "Compare the conditions and results of two runs",
		Long: "Compare two result directories: what differs in the machine, the run environment,\n" +
			"the builds, the plan and the warnings, and how the median times changed.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := loadRun(args[0])
			if err != nil {
				return err
			}
			b, err := loadRun(args[1])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "A: %s\nB: %s\n\n", args[0], args[1])
			printConditionDiff(out, a, b)
			fmt.Fprintln(out)
			return printTimeDiff(out, a, b)
		},
	}
}

func loadRun(dir string) (*runRecord, error) {
	data, err := os.ReadFile(filepath.Join(dir, results.ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("%s is not a run directory: %w", dir, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, results.ManifestFile), err)
	}
	run := &runRecord{fields: map[string]string{}, lists: map[string][]string{}, times: map[group][]time.Duration{}}
	for _, key := range skippedFields {
		delete(manifest, key)
	}
	flatten(run, "", manifest)

	records, err := results.ReadRecords(dir)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		g := group{bench: r.Bench, threads: r.Threads, alloc: r.Alloc}
		if _, seen := run.times[g]; !seen {
			run.groups = append(run.groups, g)
			run.times[g] = nil
		}
		if r.OK {
			run.times[g] = append(run.times[g], time.Duration(r.WallNs))
		}
	}
	return run, nil
}

func flatten(run *runRecord, prefix string, value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			name := key
			if prefix != "" {
				name = prefix + "." + key
			}
			flatten(run, name, item)
		}
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			items = append(items, scalar(item))
		}
		run.lists[prefix] = items
	default:
		run.fields[prefix] = scalar(v)
	}
}

func scalar(value any) string {
	switch v := value.(type) {
	case nil:
		return missingValue
	case string:
		if v == "" {
			return missingValue
		}
		if fullHash.MatchString(v) {
			return short(v)
		}
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	data, _ := json.Marshal(value)
	return string(data)
}

type conditionLine struct {
	key  string
	text string
}

func conditionDiff(a, b *runRecord) []conditionLine {
	var lines []conditionLine
	for _, key := range unionKeys(a.fields, b.fields) {
		left, right := valueOr(a.fields, key), valueOr(b.fields, key)
		if left != right {
			lines = append(lines, conditionLine{key, left + " -> " + right})
		}
	}
	for _, key := range unionKeys(a.lists, b.lists) {
		left, right := a.lists[key], b.lists[key]
		var changed []conditionLine
		for _, item := range left {
			if !slices.Contains(right, item) {
				changed = append(changed, conditionLine{key, "- " + item})
			}
		}
		for _, item := range right {
			if !slices.Contains(left, item) {
				changed = append(changed, conditionLine{key, "+ " + item})
			}
		}
		if len(changed) == 0 && !slices.Equal(left, right) {
			changed = append(changed, conditionLine{key, strings.Join(left, ", ") + " -> " + strings.Join(right, ", ")})
		}
		lines = append(lines, changed...)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].key < lines[j].key })
	return lines
}

func unionKeys[T any](a, b map[string]T) []string {
	var all []string
	for key := range a {
		all = append(all, key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			all = append(all, key)
		}
	}
	sort.Strings(all)
	return all
}

func valueOr(fields map[string]string, key string) string {
	if value, ok := fields[key]; ok {
		return value
	}
	return missingValue
}

func printConditionDiff(w io.Writer, a, b *runRecord) {
	lines := conditionDiff(a, b)
	if len(lines) == 0 {
		fmt.Fprintln(w, "conditions are the same")
		return
	}
	fmt.Fprintf(w, "conditions differ in %d places:\n", len(lines))
	width := 0
	for _, line := range lines {
		width = max(width, len(line.key))
	}
	previous := ""
	for _, line := range lines {
		key := line.key
		if key == previous {
			key = ""
		}
		previous = line.key
		fmt.Fprintf(w, "  %-*s  %s\n", width, key, line.text)
	}
}

func printTimeDiff(w io.Writer, a, b *runRecord) error {
	groups := timeGroups(a, b)
	if len(groups) == 0 {
		fmt.Fprintln(w, "no cells in either run")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCH\tTHREADS\tALLOC\tA\tB\tCHANGE")
	for _, g := range groups {
		left, right := medianOf(a.times[g]), medianOf(b.times[g])
		change := missingValue
		if left > 0 && right > 0 {
			change = fmt.Sprintf("%+.1f%%", (float64(right)/float64(left)-1)*percent)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", g.bench, g.threads, g.alloc, formatMedian(left), formatMedian(right), change)
	}
	return tw.Flush()
}

func timeGroups(a, b *runRecord) []group {
	var groups []group
	benchOrder := map[string]int{}
	allocOrder := map[string]int{}
	for _, g := range slices.Concat(a.groups, b.groups) {
		if _, ok := benchOrder[g.bench]; !ok {
			benchOrder[g.bench] = len(benchOrder)
		}
		if _, ok := allocOrder[g.alloc]; !ok {
			allocOrder[g.alloc] = len(allocOrder)
		}
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		x, y := groups[i], groups[j]
		if x.bench != y.bench {
			return benchOrder[x.bench] < benchOrder[y.bench]
		}
		if x.threads != y.threads {
			return x.threads < y.threads
		}
		return allocOrder[x.alloc] < allocOrder[y.alloc]
	})
	return groups
}

func medianOf(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	return median(values)
}

func formatMedian(d time.Duration) string {
	if d == 0 {
		return missingValue
	}
	return formatDuration(d)
}
