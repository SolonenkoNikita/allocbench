package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
)

const projectNondeterministic = `build_env = "host"
[source]
path = "files/hello"
[build]
script = "mkdir -p /out/share && cp /src/message /out/share/message && echo $JOBS > /out/share/jobs"
`

func TestOtherJobs(t *testing.T) {
	for jobs, want := range map[int]int{8: 1, 2: 1, 1: 2} {
		if got := otherJobs(jobs); got != want {
			t.Errorf("%d: got %d, want %d", jobs, got, want)
		}
	}
}

func TestVerifyTargets(t *testing.T) {
	s := &recipe.Set{
		Envs:       map[string]recipe.Env{"gcc13": {}},
		Allocators: map[string]recipe.Allocator{"mi": {}, "sys": {}},
		Benches:    map[string]recipe.Bench{"cfrac": {}},
		Tools:      map[string]recipe.Tool{"runner": {}},
	}
	refs, err := verifyTargets(s, nil)
	if err != nil || !reflect.DeepEqual(refs, []string{"allocators/mi", "allocators/sys", "benches/cfrac", "tools/runner"}) {
		t.Errorf("all: %v, %v", refs, err)
	}
	refs, err = verifyTargets(s, []string{"cfrac", "allocators/mi"})
	if err != nil || !reflect.DeepEqual(refs, []string{"benches/cfrac", "allocators/mi"}) {
		t.Errorf("args: %v, %v", refs, err)
	}
	if _, err := verifyTargets(s, []string{"gcc13"}); err == nil {
		t.Error("an env is accepted")
	}
}

func TestPrintVerdict(t *testing.T) {
	var changes []source.Change
	for i := 0; i < maxShownChanges+3; i++ {
		changes = append(changes, source.Change{Path: "f" + string(rune('a'+i)), What: "contents differ"})
	}
	tests := []struct {
		v    verdict
		want string
	}{
		{verdict{ref: "allocators/sys", nothing: true}, "allocators/sys: nothing to build\n"},
		{verdict{ref: "allocators/mi", out: strings.Repeat("e", 64)}, "allocators/mi: reproducible, out eeeeeeeeeeee\n"},
		{verdict{ref: "allocators/mi", out: strings.Repeat("e", 64), inStore: true}, "allocators/mi: reproducible, out eeeeeeeeeeee, same as the store\n"},
		{
			verdict{ref: "benches/x", changes: changes[:1], vsStore: changes[1:2], kept: "/tmp/v"},
			"benches/x: NOT reproducible\n" +
				"  two fresh builds differ in 1 paths:\n    fa: contents differ\n" +
				"  a fresh build differs from the store in 1 paths:\n    fb: contents differ\n" +
				"  outputs kept in /tmp/v\n",
		},
	}
	for _, tt := range tests {
		var out strings.Builder
		printVerdict(&out, tt.v)
		if out.String() != tt.want {
			t.Errorf("got:\n%s\nwant:\n%s", out.String(), tt.want)
		}
	}

	var out strings.Builder
	printVerdict(&out, verdict{ref: "benches/x", changes: changes})
	if !strings.Contains(out.String(), "… and 3 more") || strings.Count(out.String(), "contents differ") != maxShownChanges {
		t.Errorf("long list:\n%s", out.String())
	}
}

func TestVerifyCommand(t *testing.T) {
	_, recipes, storeDir := runProject(t)
	out, err := runCLI(t, "--recipes", recipes, "--store", storeDir, "verify", "-j", "3", "hello", "greet", "sys")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	lines := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if ref, rest, ok := strings.Cut(line, ": "); ok {
			lines[ref] = rest
		}
	}
	if got := lines["allocators/hello"]; !strings.HasPrefix(got, "reproducible, out ") || strings.Contains(got, "store") {
		t.Errorf("hello: %q", got)
	}
	if got := lines["tools/greet"]; !strings.HasPrefix(got, "reproducible, out ") || !strings.HasSuffix(got, ", same as the store") {
		t.Errorf("greet: %q", got)
	}
	if got := lines["allocators/sys"]; got != "nothing to build" {
		t.Errorf("sys: %q", got)
	}
	for _, w := range []string{"building allocators/hello with 3 jobs", "building allocators/hello with 1 jobs"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if builds, _ := filepath.Glob(filepath.Join(storeDir, "build", "*", "message")); len(builds) != 0 {
		t.Errorf("verify put the recipe into the store: %v", builds)
	}
}

func TestVerifyNotReproducible(t *testing.T) {
	_, recipes, storeDir := runProject(t)
	os.WriteFile(filepath.Join(recipes, "allocators", "jobs.toml"), []byte(projectNondeterministic), 0o644)

	out, err := runCLI(t, "--recipes", recipes, "--store", storeDir, "verify", "--keep", "-j", "4", "jobs")
	if err == nil || !strings.Contains(err.Error(), "1 of 1 recipes are not reproducible") {
		t.Fatalf("got %v\n%s", err, out)
	}
	if !strings.Contains(out, "allocators/jobs: NOT reproducible\n  two fresh builds differ in 1 paths:\n    share/jobs: contents differ\n") {
		t.Errorf("output:\n%s", out)
	}
	i := strings.Index(out, "outputs kept in ")
	if i < 0 {
		t.Fatalf("no kept dir:\n%s", out)
	}
	kept := strings.TrimSpace(strings.SplitN(out[i+len("outputs kept in "):], "\n", 2)[0])
	defer os.RemoveAll(kept)
	for n, want := range map[string]string{"build-1": "4\n", "build-2": "1\n"} {
		if data, _ := os.ReadFile(filepath.Join(kept, n, "share", "jobs")); string(data) != want {
			t.Errorf("%s: %q, want %q", n, data, want)
		}
	}
}

func TestVerifyAgainstStore(t *testing.T) {
	_, recipes, storeDir := runProject(t)
	base := []string{"--recipes", recipes, "--store", storeDir}
	if out, err := runCLI(t, append(base, "build", "hello")...); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := runCLI(t, append(base, "verify", "hello")...)
	if err != nil || !strings.Contains(out, "allocators/hello: reproducible, out ") || !strings.Contains(out, "same as the store") {
		t.Fatalf("verify: %v\n%s", err, out)
	}

	stored, _ := filepath.Glob(filepath.Join(storeDir, "build", "*", "message"))
	if len(stored) != 1 {
		t.Fatalf("store: %v", stored)
	}
	os.Chmod(stored[0], 0o644)
	os.WriteFile(stored[0], []byte("tampered\n"), 0o644)
	out, err = runCLI(t, append(base, "verify", "hello")...)
	if err == nil || !strings.Contains(out, "a fresh build differs from the store in 1 paths:\n    message: contents differ") {
		t.Errorf("tampered store: %v\n%s", err, out)
	}
	if strings.Contains(out, "two fresh builds differ") {
		t.Errorf("fresh builds differ:\n%s", out)
	}
}
