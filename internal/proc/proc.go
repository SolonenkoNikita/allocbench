package proc

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const StopDelay = 30 * time.Second

var ErrInterrupted = errors.New("interrupted")

func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = StopDelay
	return cmd
}

func Stopped(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return context.Cause(ctx)
}
