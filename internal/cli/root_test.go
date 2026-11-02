package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	cmd := NewRootCommand("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "1.2.3") {
		t.Errorf("output %q does not contain the version", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	cmd := NewRootCommand("dev")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"no-such-command"})

	if err := cmd.Execute(); err == nil {
		t.Error("expected an error for an unknown command")
	}
}
