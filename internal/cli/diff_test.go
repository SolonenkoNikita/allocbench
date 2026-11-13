package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/results"
)

func writeRun(t *testing.T, dir, manifest string, records ...results.Record) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, results.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(append(data, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, results.JSONLFile), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func record(bench, alloc string, threads int, ok bool, seconds float64) results.Record {
	return results.Record{Bench: bench, Alloc: alloc, Threads: threads, OK: ok, WallNs: int64(seconds * 1e9)}
}

const manifestA = `{
  "allocbench_version": "v1",
  "started": "2026-11-13T10:00:00Z",
  "finished": "2026-11-13T10:01:00Z",
  "plan": {"allocators": ["sys", "mi"], "benches": ["cfrac"], "repeat": 3, "cpus": "3"},
  "run_env": {"name": "gcc13", "packages": ["gcc-13", "cmake"], "hash": "` + "c4d1894efcccc7dd9ef16a93bc5072e10895f82f0624288b04f48649f160b9c4" + `"},
  "builds": {"allocators/mi": "` + "1111111111111111111111111111111111111111111111111111111111111111" + `", "allocators/sys": ""},
  "host": {"kernel": "6.14.0", "cpus": 8, "memory_kb": 15969024, "cpu_governor": "powersave"},
  "warnings": ["cpu governor: powersave on 8 of 8 CPUs", "load: 3.09 over the last minute"]
}`

const manifestB = `{
  "allocbench_version": "v1",
  "started": "2026-11-14T10:00:00Z",
  "plan": {"allocators": ["sys", "mi", "je"], "benches": ["cfrac"], "repeat": 3, "cpus": "3"},
  "run_env": {"name": "gcc13", "packages": ["cmake", "gcc-13"], "hash": "` + "c4d1894efcccc7dd9ef16a93bc5072e10895f82f0624288b04f48649f160b9c4" + `"},
  "builds": {"allocators/mi": "` + "2222222222222222222222222222222222222222222222222222222222222222" + `", "allocators/sys": "", "allocators/je": "` + "3333333333333333333333333333333333333333333333333333333333333333" + `"},
  "host": {"kernel": "6.14.0", "cpus": 8, "memory_kb": 15969024, "cpu_governor": "performance"},
  "interrupted": true,
  "warnings": ["load: 3.09 over the last minute"]
}`

func TestDiffCommand(t *testing.T) {
	root := t.TempDir()
	a := writeRun(t, filepath.Join(root, "A"), manifestA,
		record("cfrac", "sys", 1, true, 8.4),
		record("cfrac", "mi", 1, true, 7.6),
		record("cfrac", "sys", 1, true, 8.2),
		record("cfrac", "mi", 1, true, 7.7),
		record("cfrac", "sys", 1, true, 8.3),
		record("cfrac", "mi", 1, false, 0),
		record("larson", "sys", 4, false, 0),
	)
	b := writeRun(t, filepath.Join(root, "B"), manifestB,
		record("cfrac", "je", 1, true, 7.0),
		record("cfrac", "sys", 1, true, 7.47),
		record("cfrac", "mi", 1, true, 6.84),
		record("larson", "sys", 4, true, 2.5),
		record("cfrac", "sys", 2, true, 9),
	)

	out, err := runCLI(t, "diff", a, b)
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	want := "A: " + a + "\nB: " + b + "\n\n" +
		"conditions differ in 7 places:\n" +
		"  builds.allocators/je  - -> 333333333333\n" +
		"  builds.allocators/mi  111111111111 -> 222222222222\n" +
		"  host.cpu_governor     powersave -> performance\n" +
		"  interrupted           - -> true\n" +
		"  plan.allocators       + je\n" +
		"  run_env.packages      gcc-13, cmake -> cmake, gcc-13\n" +
		"  warnings              - cpu governor: powersave on 8 of 8 CPUs\n" +
		"\n" +
		"BENCH   THREADS  ALLOC  A       B       CHANGE\n" +
		"cfrac   1        sys    8.300s  7.470s  -10.0%\n" +
		"cfrac   1        mi     7.650s  6.840s  -10.6%\n" +
		"cfrac   1        je     -       7.000s  -\n" +
		"cfrac   2        sys    -       9.000s  -\n" +
		"larson  4        sys    -       2.500s  -\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestDiffSame(t *testing.T) {
	root := t.TempDir()
	a := writeRun(t, filepath.Join(root, "A"), manifestA, record("cfrac", "sys", 1, true, 1.5))
	b := writeRun(t, filepath.Join(root, "B"), strings.Replace(manifestA, "2026-11-13T10:00:00Z", "2027-01-01T00:00:00Z", 1),
		record("cfrac", "sys", 1, true, 1.5))
	out, err := runCLI(t, "diff", a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nconditions are the same\n") || !strings.Contains(out, "cfrac  1        sys    1.500s  1.500s  +0.0%") {
		t.Errorf("output:\n%s", out)
	}

	c := writeRun(t, filepath.Join(root, "C"), `{}`)
	d := writeRun(t, filepath.Join(root, "D"), `{"warnings": null}`)
	out, err = runCLI(t, "diff", c, d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "\nconditions are the same\n\nno cells in either run\n") {
		t.Errorf("empty runs:\n%s", out)
	}
}

func TestDiffErrors(t *testing.T) {
	root := t.TempDir()
	good := writeRun(t, filepath.Join(root, "good"), `{}`)
	badJSON := writeRun(t, filepath.Join(root, "json"), `{`)
	noRecords := writeRun(t, filepath.Join(root, "norecords"), `{}`)
	os.Remove(filepath.Join(noRecords, results.JSONLFile))
	badRecords := writeRun(t, filepath.Join(root, "records"), `{}`)
	os.WriteFile(filepath.Join(badRecords, results.JSONLFile), []byte("{\"bench\":\"x\"}\nnot json\n"), 0o644)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"diff", good}, "accepts 2 arg(s)"},
		{[]string{"diff", filepath.Join(root, "missing"), good}, "missing is not a run directory"},
		{[]string{"diff", good, filepath.Join(root, "missing")}, "missing is not a run directory"},
		{[]string{"diff", badJSON, good}, filepath.Join(badJSON, results.ManifestFile)},
		{[]string{"diff", noRecords, good}, results.JSONLFile},
		{[]string{"diff", good, badRecords}, filepath.Join(badRecords, results.JSONLFile)},
	} {
		if out, err := runCLI(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: got %v, want %q\n%s", tc.args[1:], err, tc.want, out)
		}
	}
}

func TestScalar(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{nil, "-"},
		{"", "-"},
		{"powersave", "powersave"},
		{strings.Repeat("ab", 32), strings.Repeat("ab", 6)},
		{strings.Repeat("AB", 32), strings.Repeat("AB", 32)},
		{float64(15969024), "15969024"},
		{1.5, "1.5"},
		{true, "true"},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
	} {
		if got := scalar(tc.value); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestFlattenListOfObjects(t *testing.T) {
	run := &runRecord{fields: map[string]string{}, lists: map[string][]string{}}
	flatten(run, "", map[string]any{"a": map[string]any{"b": []any{map[string]any{"x": 1.0}, "y"}}})
	if got := run.lists["a.b"]; len(got) != 2 || got[0] != `{"x":1}` || got[1] != "y" {
		t.Errorf("lists: %v", run.lists)
	}
}
