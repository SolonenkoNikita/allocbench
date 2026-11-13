package plan

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCPUs(t *testing.T) {
	for text, want := range map[string][]int{
		"":              nil,
		"  ":            nil,
		"3":             {3},
		"2-5":           {2, 3, 4, 5},
		"0,2-3,7":       {0, 2, 3, 7},
		" 6 , 1 - 2 ":   {1, 2, 6},
		"4-4":           {4},
		"3,1-4,2":       {1, 2, 3, 4},
		"1023":          {1023},
		"007":           {7},
		"1020-1023,0-1": {0, 1, 1020, 1021, 1022, 1023},
	} {
		got, err := ParseCPUs(text)
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func TestParseCPUsErrors(t *testing.T) {
	for text, want := range map[string]string{
		"a":                    `cpus "a": "a" is not a CPU number, write a list like 0-3,6`,
		"1,":                   `cpus "1,": "" is not a CPU number`,
		",1":                   `cpus ",1": "" is not a CPU number`,
		"1-":                   `cpus "1-": "" is not a CPU number`,
		"-1":                   `cpus "-1": "" is not a CPU number`,
		"1-2-3":                `"2-3" is not a CPU number`,
		"5-2":                  `cpus "5-2": range 5-2 goes down`,
		"1024":                 `cpus "1024": CPU 1024 is above 1023`,
		"0-99999":              `CPU 99999 is above 1023`,
		"1;2":                  `"1;2" is not a CPU number`,
		"+1":                   `"+1" is not a CPU number`,
		"99999999999999999999": "is above 1023",
	} {
		_, err := ParseCPUs(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

func TestFormatCPUs(t *testing.T) {
	for want, cpus := range map[string][]int{
		"":          nil,
		"3":         {3},
		"2-5":       {2, 3, 4, 5},
		"0,2-3,7":   {0, 2, 3, 7},
		"0-1,4,6-8": {0, 1, 4, 6, 7, 8},
		"1,3,5":     {1, 3, 5},
		"1020-1023": {1020, 1021, 1022, 1023},
	} {
		if got := FormatCPUs(cpus); got != want {
			t.Errorf("%v: got %q, want %q", cpus, got, want)
		}
		parsed, err := ParseCPUs(want)
		if err != nil || !reflect.DeepEqual(parsed, cpus) {
			t.Errorf("%q does not parse back: %v, %v", want, parsed, err)
		}
	}
}
