package plan

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const MaxCPU = 1023

func ParseCPUs(text string) ([]int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var cpus []int
	for _, item := range strings.Split(text, ",") {
		first, last, isRange := strings.Cut(strings.TrimSpace(item), "-")
		low, err := parseCPU(first)
		if err != nil {
			return nil, fmt.Errorf("cpus %q: %w", text, err)
		}
		high := low
		if isRange {
			if high, err = parseCPU(last); err != nil {
				return nil, fmt.Errorf("cpus %q: %w", text, err)
			}
			if high < low {
				return nil, fmt.Errorf("cpus %q: range %d-%d goes down", text, low, high)
			}
		}
		for cpu := low; cpu <= high; cpu++ {
			cpus = append(cpus, cpu)
		}
	}
	slices.Sort(cpus)
	return slices.Compact(cpus), nil
}

func parseCPU(text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, fmt.Errorf("%q is not a CPU number, write a list like 0-3,6", text)
	}
	cpu, err := strconv.Atoi(text)
	if err != nil || cpu > MaxCPU {
		return 0, fmt.Errorf("CPU %s is above %d", text, MaxCPU)
	}
	return cpu, nil
}

func FormatCPUs(cpus []int) string {
	var parts []string
	for start := 0; start < len(cpus); {
		end := start
		for end+1 < len(cpus) && cpus[end+1] == cpus[end]+1 {
			end++
		}
		part := strconv.Itoa(cpus[start])
		if end > start {
			part += "-" + strconv.Itoa(cpus[end])
		}
		parts = append(parts, part)
		start = end + 1
	}
	return strings.Join(parts, ",")
}
