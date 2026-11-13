package proc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandStopsWithTerm(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	out := filepath.Join(t.TempDir(), "out")
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	cmd := Command(ctx, "/bin/sh", "-c", `trap 'kill $!; echo term; exit 7' TERM; sleep 30 & wait`)
	cmd.Stdout = file
	if cmd.WaitDelay != StopDelay {
		t.Errorf("WaitDelay %v", cmd.WaitDelay)
	}
	time.AfterFunc(300*time.Millisecond, func() { cancel(ErrInterrupted) })
	start := time.Now()
	err = cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Errorf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("stopped after %v", elapsed)
	}
	if data, _ := os.ReadFile(out); string(data) != "term\n" {
		t.Errorf("output %q", data)
	}
}

func TestCommandWithoutCancel(t *testing.T) {
	if err := Command(context.Background(), "/bin/sh", "-c", "exit 0").Run(); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestStopped(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	if err := Stopped(ctx); err != nil {
		t.Errorf("running: %v", err)
	}
	cancel(ErrInterrupted)
	if err := Stopped(ctx); !errors.Is(err, ErrInterrupted) || err.Error() != "interrupted" {
		t.Errorf("interrupted: %v", err)
	}

	plain, stop := context.WithCancel(context.Background())
	stop()
	if err := Stopped(plain); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
