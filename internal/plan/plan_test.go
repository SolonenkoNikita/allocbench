package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writePlan(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFull(t *testing.T) {
	path := writePlan(t, `
allocators = ["sys", "mi"]
benches    = ["cfrac"]
run_env    = "run"
repeat     = 5
cpus       = "2-9"
order      = "sequential"
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &Plan{
		Allocators: []string{"sys", "mi"},
		Benches:    []string{"cfrac"},
		RunEnv:     "run",
		Repeat:     5,
		CPUs:       "2-9",
		Order:      OrderSequential,
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	p, err := Load(writePlan(t, `allocators = ["mi"]`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Repeat != 1 || p.Order != OrderInterleaved {
		t.Errorf("defaults not kept: %+v", p)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown field", `alocators = ["mi"]`, "unknown fields: alocators"},
		{"syntax", `allocators = ["mi"`, "expected a comma"},
		{"wrong type", `repeat = "five"`, "repeat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writePlan(t, tt.content)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain the path and %q", err, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("expected an error")
	}
}
