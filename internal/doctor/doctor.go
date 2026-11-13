package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/SolonenkoNikita/allocbench/internal/results"
)

type Level int

const (
	OK Level = iota
	Warning
	Error
)

func (l Level) String() string {
	switch l {
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return "ok"
}

type Finding struct {
	Level  Level
	Check  string
	Detail string
	Fix    string
}

const (
	minKernelMajor  = 5
	minKernelMinor  = 3
	subIDCount      = 65536
	firstSubID      = 100000
	minAvailableKB  = 2 * 1024 * 1024
	kbPerGB         = 1024 * 1024
	maxLoad         = 1.0
	governorWanted  = "performance"
	thpNoisy        = "always"
	batteryType     = "Battery"
	batteryDraining = "Discharging"
)

var releasePattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)`)

type Checker struct {
	Root     string
	User     string
	UID      string
	LookPath func(name string) (string, error)
	Run      func(name string, args ...string) ([]byte, error)
}

func New() *Checker {
	uid := strconv.Itoa(os.Getuid())
	name := os.Getenv("USER")
	if u, err := user.LookupId(uid); err == nil {
		name = u.Username
	}
	return &Checker{
		Root:     "/",
		User:     name,
		UID:      uid,
		LookPath: exec.LookPath,
		Run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
	}
}

func (c *Checker) Check() []Finding {
	return append(c.Requirements(), c.Conditions()...)
}

func (c *Checker) Requirements() []Finding {
	return collect(c.kernel, c.tools, c.userns, c.subIDs)
}

func (c *Checker) Conditions() []Finding {
	return collect(c.governor, c.turbo, c.corePattern, c.hugePages, c.memory, c.load, c.power)
}

func collect(checks ...func() []Finding) []Finding {
	var findings []Finding
	for _, check := range checks {
		findings = append(findings, check()...)
	}
	return findings
}

func ok(check, detail string) []Finding {
	return []Finding{{Level: OK, Check: check, Detail: detail}}
}

func warn(check, detail, fix string) []Finding {
	return []Finding{{Level: Warning, Check: check, Detail: detail, Fix: fix}}
}

func fail(check, detail, fix string) []Finding {
	return []Finding{{Level: Error, Check: check, Detail: detail, Fix: fix}}
}

func (c *Checker) path(name string) string {
	return filepath.Join(c.Root, name)
}

func (c *Checker) read(name string) (string, bool) {
	return readFile(c.path(name))
}

func readFile(name string) (string, bool) {
	data, err := os.ReadFile(name)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func (c *Checker) kernel() []Finding {
	const check = "kernel"
	release, found := c.read("proc/sys/kernel/osrelease")
	if !found {
		return fail(check, "cannot read the kernel release", "")
	}
	match := releasePattern.FindStringSubmatch(release)
	if match == nil {
		return fail(check, fmt.Sprintf("cannot parse the kernel release %q", release), "")
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major < minKernelMajor || major == minKernelMajor && minor < minKernelMinor {
		return fail(check, fmt.Sprintf("%s is older than %d.%d, the runner needs pidfd_open", release, minKernelMajor, minKernelMinor), "")
	}
	return ok(check, release)
}

var requiredTools = []struct {
	name    string
	pkg     string
	purpose string
}{
	{"bwrap", "bubblewrap", "sandboxes for builds and runs"},
	{"mmdebstrap", "mmdebstrap", "creating environments"},
	{"git", "git", "fetching sources"},
}

func (c *Checker) tools() []Finding {
	var findings []Finding
	for _, tool := range requiredTools {
		found, err := c.LookPath(tool.name)
		if err != nil {
			findings = append(findings, fail(tool.name, "not found, it is needed for "+tool.purpose, "sudo apt install "+tool.pkg)...)
			continue
		}
		findings = append(findings, ok(tool.name, found)...)
	}
	return findings
}

func (c *Checker) userns() []Finding {
	const check = "user namespaces"
	if value, found := c.read("proc/sys/kernel/unprivileged_userns_clone"); found && value == "0" {
		return fail(check, "disabled by kernel.unprivileged_userns_clone", "sudo sysctl kernel.unprivileged_userns_clone=1")
	}
	if value, found := c.read("proc/sys/user/max_user_namespaces"); found && value == "0" {
		return fail(check, "disabled by user.max_user_namespaces", "sudo sysctl user.max_user_namespaces=15000")
	}
	bwrap, err := c.LookPath("bwrap")
	if err != nil {
		return nil
	}
	output, err := c.Run(bwrap, "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "true")
	if err != nil {
		fix := ""
		if value, _ := c.read("proc/sys/kernel/apparmor_restrict_unprivileged_userns"); value == "1" {
			fix = "sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0"
		}
		return fail(check, "bwrap cannot create a sandbox: "+firstLine(output, err), fix)
	}
	return ok(check, "bwrap creates sandboxes")
}

func firstLine(output []byte, err error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	if line == "" {
		return err.Error()
	}
	return line
}

func (c *Checker) subIDs() []Finding {
	var findings []Finding
	for _, file := range []string{"subuid", "subgid"} {
		findings = append(findings, c.subID(file)...)
	}
	return findings
}

func (c *Checker) subID(file string) []Finding {
	check := "/etc/" + file
	data, err := os.ReadFile(c.path(filepath.Join("etc", file)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(check, err.Error(), "")
	}
	free := firstSubID
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) != 3 {
			continue
		}
		start, startErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[2])
		if startErr != nil || countErr != nil {
			continue
		}
		free = max(free, start+count)
		if (fields[0] == c.User || fields[0] == c.UID) && count >= subIDCount {
			return ok(check, strings.TrimSpace(line))
		}
	}
	return fail(check,
		fmt.Sprintf("no range of %d ids for %s, mmdebstrap cannot create environments", subIDCount, c.User),
		fmt.Sprintf("echo %s:%d:%d | sudo tee -a /etc/%s", c.User, free, subIDCount, file))
}

func (c *Checker) governor() []Finding {
	const check = "cpu governor"
	paths, _ := filepath.Glob(c.path("sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_governor"))
	var others []string
	slow := 0
	for _, name := range paths {
		value, found := readFile(name)
		if !found || value == governorWanted {
			continue
		}
		slow++
		if !slices.Contains(others, value) {
			others = append(others, value)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if slow == 0 {
		return ok(check, fmt.Sprintf("%s on %d CPUs", governorWanted, len(paths)))
	}
	return warn(check,
		fmt.Sprintf("%s on %d of %d CPUs, the frequency follows the load", strings.Join(others, ", "), slow, len(paths)),
		"echo "+governorWanted+" | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor")
}

func (c *Checker) turbo() []Finding {
	const check = "turbo boost"
	const detail = "enabled, the frequency depends on temperature"
	if value, found := c.read("sys/devices/system/cpu/intel_pstate/no_turbo"); found {
		if value == "0" {
			return warn(check, detail, "echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo")
		}
		return ok(check, "disabled")
	}
	if value, found := c.read("sys/devices/system/cpu/cpufreq/boost"); found {
		if value == "1" {
			return warn(check, detail, "echo 0 | sudo tee /sys/devices/system/cpu/cpufreq/boost")
		}
		return ok(check, "disabled")
	}
	return nil
}

func (c *Checker) corePattern() []Finding {
	const check = "core dumps"
	pattern, found := c.read("proc/sys/kernel/core_pattern")
	if !found {
		return nil
	}
	handler, piped := strings.CutPrefix(pattern, "|")
	if !piped {
		return ok(check, pattern)
	}
	program, _, _ := strings.Cut(strings.TrimSpace(handler), " ")
	fix := "sudo sysctl kernel.core_pattern=core"
	if strings.Contains(program, "apport") {
		fix = "sudo systemctl disable --now apport"
	}
	return warn(check, "piped to "+program+", a crashed cell waits for it for seconds", fix)
}

func (c *Checker) hugePages() []Finding {
	const check = "huge pages"
	mode := results.ReadHost(c.Root).THP
	switch mode {
	case "":
		return nil
	case thpNoisy:
		return warn(check, thpNoisy+", results depend on how many huge pages are free",
			"echo madvise | sudo tee /sys/kernel/mm/transparent_hugepage/enabled")
	}
	return ok(check, mode)
}

func (c *Checker) memory() []Finding {
	const check = "memory"
	available, found := c.meminfo("MemAvailable")
	if !found {
		return nil
	}
	detail := fmt.Sprintf("%.1f GB available", float64(available)/kbPerGB)
	if available < minAvailableKB {
		return warn(check, detail+", the kernel may reclaim pages during runs", "close programs that use a lot of memory")
	}
	return ok(check, detail)
}

func (c *Checker) meminfo(key string) (int64, bool) {
	data, found := c.read("proc/meminfo")
	if !found {
		return 0, false
	}
	for _, line := range strings.Split(data, "\n") {
		name, value, cut := strings.Cut(line, ":")
		if !cut || name != key {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "kB")), 10, 64)
		return kb, err == nil
	}
	return 0, false
}

func (c *Checker) load() []Finding {
	const check = "load"
	data, found := c.read("proc/loadavg")
	if !found {
		return nil
	}
	fields := strings.Fields(data)
	if len(fields) == 0 {
		return nil
	}
	average, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil
	}
	detail := fmt.Sprintf("%.2f over the last minute", average)
	if average > maxLoad {
		return warn(check, detail+", other processes compete for CPUs", "wait for background jobs to finish")
	}
	return ok(check, detail)
}

func (c *Checker) power() []Finding {
	const check = "power"
	types, _ := filepath.Glob(c.path("sys/class/power_supply/*/type"))
	batteries := 0
	for _, name := range types {
		kind, _ := readFile(name)
		if kind != batteryType {
			continue
		}
		batteries++
		if status, _ := readFile(filepath.Join(filepath.Dir(name), "status")); status == batteryDraining {
			return warn(check, "on battery, the CPU may be throttled", "connect the charger")
		}
	}
	if batteries == 0 {
		return nil
	}
	return ok(check, "on mains")
}
