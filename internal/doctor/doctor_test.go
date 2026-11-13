package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var healthyFiles = map[string]string{
	"proc/sys/kernel/osrelease":                 "6.14.0-36-generic\n",
	"proc/sys/kernel/unprivileged_userns_clone": "1\n",
	"proc/sys/user/max_user_namespaces":         "61563\n",
	"proc/sys/kernel/core_pattern":              "core\n",
	"proc/meminfo":                              "MemTotal:       16000000 kB\nMemAvailable:    8388608 kB\n",
	"proc/loadavg":                              "0.25 0.30 0.40 1/500 1234\n",
	"etc/subuid":                                "omp:100000:65536\nbench:165536:65536\n",
	"etc/subgid":                                "omp:100000:65536\nbench:165536:65536\n",
	"sys/devices/system/cpu/cpu0/cpufreq/scaling_governor": "performance\n",
	"sys/devices/system/cpu/cpu1/cpufreq/scaling_governor": "performance\n",
	"sys/devices/system/cpu/intel_pstate/no_turbo":         "1\n",
	"sys/kernel/mm/transparent_hugepage/enabled":           "always [madvise] never\n",
	"sys/class/power_supply/AC/type":                       "Mains\n",
	"sys/class/power_supply/BAT0/type":                     "Battery\n",
	"sys/class/power_supply/BAT0/status":                   "Full\n",
}

type fakeHost struct {
	missing []string
	runs    [][]string
	output  string
	runErr  error
}

func newChecker(t *testing.T, files map[string]string) (*Checker, *fakeHost) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := &fakeHost{}
	c := &Checker{
		Root: root,
		User: "bench",
		UID:  "1000",
		LookPath: func(name string) (string, error) {
			for _, m := range host.missing {
				if m == name {
					return "", exec.ErrNotFound
				}
			}
			return "/usr/bin/" + name, nil
		},
		Run: func(name string, args ...string) ([]byte, error) {
			host.runs = append(host.runs, append([]string{name}, args...))
			return []byte(host.output), host.runErr
		},
	}
	return c, host
}

func with(files map[string]string, changes map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	for k, v := range changes {
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

func find(t *testing.T, findings []Finding, check string) Finding {
	t.Helper()
	for _, f := range findings {
		if f.Check == check {
			return f
		}
	}
	t.Fatalf("no finding %q in %v", check, findings)
	return Finding{}
}

func absent(t *testing.T, findings []Finding, check string) {
	t.Helper()
	for _, f := range findings {
		if f.Check == check {
			t.Errorf("unexpected finding %+v", f)
		}
	}
}

func TestLevelString(t *testing.T) {
	for level, want := range map[Level]string{OK: "ok", Warning: "warning", Error: "error"} {
		if got := level.String(); got != want {
			t.Errorf("%d: got %q, want %q", level, got, want)
		}
	}
}

func TestHealthyMachine(t *testing.T) {
	c, host := newChecker(t, healthyFiles)
	findings := c.Check()

	var checks []string
	for _, f := range findings {
		checks = append(checks, f.Check)
		if f.Level != OK || f.Fix != "" {
			t.Errorf("not ok: %+v", f)
		}
	}
	want := []string{
		"kernel", "bwrap", "mmdebstrap", "git", "user namespaces", "/etc/subuid", "/etc/subgid",
		"cpu governor", "turbo boost", "core dumps", "huge pages", "memory", "load", "power",
	}
	if !reflect.DeepEqual(checks, want) {
		t.Errorf("checks:\n got %v\nwant %v", checks, want)
	}

	details := map[string]string{
		"kernel":          "6.14.0-36-generic",
		"bwrap":           "/usr/bin/bwrap",
		"user namespaces": "bwrap creates sandboxes",
		"/etc/subuid":     "bench:165536:65536",
		"cpu governor":    "performance on 2 CPUs",
		"turbo boost":     "disabled",
		"core dumps":      "core",
		"huge pages":      "madvise",
		"memory":          "8.0 GB available",
		"load":            "0.25 over the last minute",
		"power":           "on mains",
	}
	for check, detail := range details {
		if got := find(t, findings, check).Detail; got != detail {
			t.Errorf("%s: got %q, want %q", check, got, detail)
		}
	}

	wantRun := [][]string{{"/usr/bin/bwrap", "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "true"}}
	if !reflect.DeepEqual(host.runs, wantRun) {
		t.Errorf("runs: %v", host.runs)
	}
}

func TestRequirementsAndConditions(t *testing.T) {
	c, _ := newChecker(t, healthyFiles)
	names := func(findings []Finding) []string {
		var out []string
		for _, f := range findings {
			out = append(out, f.Check)
		}
		return out
	}
	if got := names(c.Requirements()); !reflect.DeepEqual(got, []string{"kernel", "bwrap", "mmdebstrap", "git", "user namespaces", "/etc/subuid", "/etc/subgid"}) {
		t.Errorf("requirements: %v", got)
	}
	if got := names(c.Conditions()); !reflect.DeepEqual(got, []string{"cpu governor", "turbo boost", "core dumps", "huge pages", "memory", "load", "power"}) {
		t.Errorf("conditions: %v", got)
	}
}

func TestEmptyRoot(t *testing.T) {
	c, _ := newChecker(t, nil)
	findings := c.Check()
	if f := find(t, findings, "kernel"); f.Level != Error || f.Detail != "cannot read the kernel release" {
		t.Errorf("kernel: %+v", f)
	}
	if f := find(t, findings, "/etc/subuid"); f.Level != Error || f.Fix != "echo bench:100000:65536 | sudo tee -a /etc/subuid" {
		t.Errorf("subuid: %+v", f)
	}
	for _, check := range []string{"cpu governor", "turbo boost", "core dumps", "huge pages", "memory", "load", "power"} {
		absent(t, findings, check)
	}
}

func TestKernel(t *testing.T) {
	for release, want := range map[string]Level{
		"5.3.0":            OK,
		"5.10.0-1-amd64":   OK,
		"6.0":              OK,
		"5.2.21":           Error,
		"4.19.0":           Error,
		"not-a-release":    Error,
		"5":                Error,
		"10.1.0-something": OK,
	} {
		c, _ := newChecker(t, map[string]string{"proc/sys/kernel/osrelease": release})
		f := c.kernel()[0]
		if f.Level != want {
			t.Errorf("%s: %+v", release, f)
		}
	}

	c, _ := newChecker(t, map[string]string{"proc/sys/kernel/osrelease": "5.2.0"})
	if f := c.kernel()[0]; f.Detail != "5.2.0 is older than 5.3, the runner needs pidfd_open" {
		t.Errorf("old kernel: %q", f.Detail)
	}
}

func TestMissingTools(t *testing.T) {
	c, host := newChecker(t, healthyFiles)
	host.missing = []string{"bwrap", "git"}
	findings := c.Check()

	f := find(t, findings, "bwrap")
	if f.Level != Error || f.Detail != "not found, it is needed for sandboxes for builds and runs" || f.Fix != "sudo apt install bubblewrap" {
		t.Errorf("bwrap: %+v", f)
	}
	if f := find(t, findings, "git"); f.Level != Error || f.Fix != "sudo apt install git" {
		t.Errorf("git: %+v", f)
	}
	if f := find(t, findings, "mmdebstrap"); f.Level != OK {
		t.Errorf("mmdebstrap: %+v", f)
	}
	absent(t, findings, "user namespaces")
	if len(host.runs) != 0 {
		t.Errorf("bwrap was run: %v", host.runs)
	}
}

func TestUserNamespaces(t *testing.T) {
	c, host := newChecker(t, with(healthyFiles, map[string]string{"proc/sys/kernel/unprivileged_userns_clone": "0\n"}))
	if f := c.userns()[0]; f.Level != Error || f.Fix != "sudo sysctl kernel.unprivileged_userns_clone=1" {
		t.Errorf("userns_clone: %+v", f)
	}
	if len(host.runs) != 0 {
		t.Errorf("bwrap was run: %v", host.runs)
	}

	c, _ = newChecker(t, with(healthyFiles, map[string]string{"proc/sys/user/max_user_namespaces": "0\n"}))
	if f := c.userns()[0]; f.Level != Error || f.Fix != "sudo sysctl user.max_user_namespaces=15000" {
		t.Errorf("max_user_namespaces: %+v", f)
	}

	c, host = newChecker(t, with(healthyFiles, map[string]string{"proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n"}))
	host.output = "bwrap: setting up uid map: Permission denied\nsecond line\n"
	host.runErr = errors.New("exit status 1")
	f := c.userns()[0]
	if f.Level != Error || f.Detail != "bwrap cannot create a sandbox: bwrap: setting up uid map: Permission denied" ||
		f.Fix != "sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0" {
		t.Errorf("bwrap fails: %+v", f)
	}

	c, host = newChecker(t, healthyFiles)
	host.runErr = errors.New("signal: killed")
	if f := c.userns()[0]; f.Detail != "bwrap cannot create a sandbox: signal: killed" || f.Fix != "" {
		t.Errorf("bwrap without output: %+v", f)
	}
}

func TestSubIDs(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		level   Level
		detail  string
		fix     string
	}{
		"by name": {content: "bench:200000:65536\n", level: OK, detail: "bench:200000:65536"},
		"by uid":  {content: "1000:200000:70000\n", level: OK, detail: "1000:200000:70000"},
		"second":  {content: "bench:200000:10\nbench:300000:65536\n", level: OK, detail: "bench:300000:65536"},
		"small": {
			content: "omp:100000:65536\nbench:300000:10\nbad line\nx:y:z\n",
			level:   Error,
			detail:  "no range of 65536 ids for bench, mmdebstrap cannot create environments",
			fix:     "echo bench:300010:65536 | sudo tee -a /etc/subuid",
		},
		"other users": {
			content: "omp:100000:65536\nuser:165536:65536\n",
			level:   Error,
			fix:     "echo bench:231072:65536 | sudo tee -a /etc/subuid",
		},
		"empty": {content: "", level: Error, fix: "echo bench:100000:65536 | sudo tee -a /etc/subuid"},
	} {
		c, _ := newChecker(t, map[string]string{"etc/subuid": tc.content})
		f := c.subID("subuid")[0]
		if f.Level != tc.level || f.Check != "/etc/subuid" {
			t.Errorf("%s: %+v", name, f)
		}
		if tc.detail != "" && f.Detail != tc.detail {
			t.Errorf("%s: detail %q", name, f.Detail)
		}
		if f.Fix != tc.fix {
			t.Errorf("%s: fix %q", name, f.Fix)
		}
	}

	c, _ := newChecker(t, map[string]string{"etc/subuid": "bench:100000:65536\n"})
	findings := c.subIDs()
	if len(findings) != 2 || findings[0].Level != OK || findings[1].Level != Error || findings[1].Fix != "echo bench:100000:65536 | sudo tee -a /etc/subgid" {
		t.Errorf("subids: %+v", findings)
	}

	c, _ = newChecker(t, map[string]string{"etc/subuid/inner": ""})
	if f := c.subID("subuid")[0]; f.Level != Error || f.Fix != "" {
		t.Errorf("unreadable: %+v", f)
	}
}

func TestGovernor(t *testing.T) {
	c, _ := newChecker(t, with(healthyFiles, map[string]string{
		"sys/devices/system/cpu/cpu1/cpufreq/scaling_governor":  "powersave\n",
		"sys/devices/system/cpu/cpu2/cpufreq/scaling_governor":  "schedutil\n",
		"sys/devices/system/cpu/cpu3/cpufreq/scaling_governor":  "powersave\n",
		"sys/devices/system/cpu/cpufreq/policy0/scaling_driver": "intel_pstate\n",
	}))
	f := c.governor()[0]
	if f.Level != Warning || f.Detail != "powersave, schedutil on 3 of 4 CPUs, the frequency follows the load" ||
		f.Fix != "echo performance | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor" {
		t.Errorf("governor: %+v", f)
	}
}

func TestTurbo(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		level Level
		fix   string
	}{
		"intel on":  {map[string]string{"sys/devices/system/cpu/intel_pstate/no_turbo": "0\n"}, Warning, "echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo"},
		"intel off": {map[string]string{"sys/devices/system/cpu/intel_pstate/no_turbo": "1\n"}, OK, ""},
		"boost on":  {map[string]string{"sys/devices/system/cpu/cpufreq/boost": "1\n"}, Warning, "echo 0 | sudo tee /sys/devices/system/cpu/cpufreq/boost"},
		"boost off": {map[string]string{"sys/devices/system/cpu/cpufreq/boost": "0\n"}, OK, ""},
		"intel first": {map[string]string{
			"sys/devices/system/cpu/intel_pstate/no_turbo": "1\n",
			"sys/devices/system/cpu/cpufreq/boost":         "1\n",
		}, OK, ""},
	} {
		c, _ := newChecker(t, tc.files)
		f := c.turbo()[0]
		if f.Level != tc.level || f.Fix != tc.fix {
			t.Errorf("%s: %+v", name, f)
		}
	}
	c, _ := newChecker(t, nil)
	if findings := c.turbo(); len(findings) != 0 {
		t.Errorf("no cpufreq: %+v", findings)
	}
}

func TestCorePattern(t *testing.T) {
	for pattern, want := range map[string]Finding{
		"|/usr/share/apport/apport -p%p -s%s -c%c -d%d -P%P -u%u -g%g -F%F -- %E": {
			Level:  Warning,
			Check:  "core dumps",
			Detail: "piped to /usr/share/apport/apport, a crashed cell waits for it for seconds",
			Fix:    "sudo systemctl disable --now apport",
		},
		"|/usr/lib/systemd/systemd-coredump %P %u %g %s %t %c %h": {
			Level:  Warning,
			Check:  "core dumps",
			Detail: "piped to /usr/lib/systemd/systemd-coredump, a crashed cell waits for it for seconds",
			Fix:    "sudo sysctl kernel.core_pattern=core",
		},
		"core.%p": {Level: OK, Check: "core dumps", Detail: "core.%p"},
	} {
		c, _ := newChecker(t, map[string]string{"proc/sys/kernel/core_pattern": pattern + "\n"})
		if got := c.corePattern()[0]; got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", pattern, got, want)
		}
	}
}

func TestHugePages(t *testing.T) {
	c, _ := newChecker(t, map[string]string{"sys/kernel/mm/transparent_hugepage/enabled": "[always] madvise never\n"})
	f := c.hugePages()[0]
	if f.Level != Warning || f.Detail != "always, results depend on how many huge pages are free" ||
		f.Fix != "echo madvise | sudo tee /sys/kernel/mm/transparent_hugepage/enabled" {
		t.Errorf("always: %+v", f)
	}

	c, _ = newChecker(t, map[string]string{"sys/kernel/mm/transparent_hugepage/enabled": "always madvise [never]\n"})
	if f := c.hugePages()[0]; f.Level != OK || f.Detail != "never" {
		t.Errorf("never: %+v", f)
	}
}

func TestMemory(t *testing.T) {
	c, _ := newChecker(t, map[string]string{"proc/meminfo": "MemTotal: 4000000 kB\nMemFree: 100 kB\nMemAvailable:    1048576 kB\n"})
	f := c.memory()[0]
	if f.Level != Warning || f.Detail != "1.0 GB available, the kernel may reclaim pages during runs" || f.Fix == "" {
		t.Errorf("low: %+v", f)
	}

	for _, content := range []string{"MemTotal: 4000000 kB\n", "MemAvailable: lots kB\n"} {
		c, _ := newChecker(t, map[string]string{"proc/meminfo": content})
		if findings := c.memory(); len(findings) != 0 {
			t.Errorf("%q: %+v", content, findings)
		}
	}
}

func TestLoad(t *testing.T) {
	c, _ := newChecker(t, map[string]string{"proc/loadavg": "3.07 2.28 1.59 1/2506 729443\n"})
	f := c.load()[0]
	if f.Level != Warning || f.Detail != "3.07 over the last minute, other processes compete for CPUs" || f.Fix == "" {
		t.Errorf("busy: %+v", f)
	}

	c, _ = newChecker(t, map[string]string{"proc/loadavg": "1.00 0 0 1/1 1\n"})
	if f := c.load()[0]; f.Level != OK {
		t.Errorf("at the limit: %+v", f)
	}

	for _, content := range []string{"\n", "high 1 1\n"} {
		c, _ := newChecker(t, map[string]string{"proc/loadavg": content})
		if findings := c.load(); len(findings) != 0 {
			t.Errorf("%q: %+v", content, findings)
		}
	}
}

func TestPower(t *testing.T) {
	c, _ := newChecker(t, with(healthyFiles, map[string]string{"sys/class/power_supply/BAT0/status": "Discharging\n"}))
	f := c.power()[0]
	if f.Level != Warning || f.Detail != "on battery, the CPU may be throttled" || f.Fix != "connect the charger" {
		t.Errorf("battery: %+v", f)
	}

	c, _ = newChecker(t, map[string]string{"sys/class/power_supply/AC/type": "Mains\n"})
	if findings := c.power(); len(findings) != 0 {
		t.Errorf("no battery: %+v", findings)
	}
}

func TestNew(t *testing.T) {
	c := New()
	if c.Root != "/" || c.UID == "" || c.LookPath == nil || c.Run == nil {
		t.Errorf("checker: %+v", c)
	}
	if c.User == "" && os.Getenv("USER") != "" {
		t.Error("user name is empty")
	}
	output, err := c.Run("sh", "-c", "echo out; echo err >&2")
	if err != nil || !strings.Contains(string(output), "out") || !strings.Contains(string(output), "err") {
		t.Errorf("run: %q %v", output, err)
	}
}
