package cli

import (
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/doctor"
)

func TestPrintFindings(t *testing.T) {
	findings := []doctor.Finding{
		{Level: doctor.OK, Check: "kernel", Detail: "6.14.0"},
		{Level: doctor.Warning, Check: "cpu governor", Detail: "powersave on 8 of 8 CPUs", Fix: "echo performance"},
		{Level: doctor.Error, Check: "git", Detail: "not found", Fix: "sudo apt install git"},
	}
	var out strings.Builder
	err := printFindings(&out, findings)
	want := "ok       kernel        6.14.0\n" +
		"warning  cpu governor  powersave on 8 of 8 CPUs\n" +
		"                       fix: echo performance\n" +
		"error    git           not found\n" +
		"                       fix: sudo apt install git\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	if err == nil || err.Error() != "1 of 3 checks failed, allocbench cannot work on this machine" {
		t.Errorf("error: %v", err)
	}

	out.Reset()
	if err := printFindings(&out, findings[:2]); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n1 of 2 checks gave warnings, measurements will be noisy\n") {
		t.Errorf("warnings:\n%s", out.String())
	}

	out.Reset()
	if err := printFindings(&out, findings[:1]); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ok       kernel  6.14.0\nthe machine is ready\n" {
		t.Errorf("ready:\n%s", out.String())
	}
}

func TestDoctorCommand(t *testing.T) {
	out, err := runCLI(t, "doctor")
	if !strings.Contains(out, "kernel") || !strings.Contains(out, "bwrap") {
		t.Errorf("output: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "doctor", "extra"); err == nil {
		t.Error("expected an error for an argument")
	}
}
