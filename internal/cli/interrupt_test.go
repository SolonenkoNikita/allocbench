package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/proc"
)

func TestInterruptContext(t *testing.T) {
	for _, first := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		var out bytes.Buffer
		exits := make(chan int, 1)
		ctx, stop := interruptContext(&out, func(code int) { exits <- code })

		if err := syscall.Kill(os.Getpid(), first); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not cancel the context", first)
		}
		if cause := context.Cause(ctx); !errors.Is(cause, proc.ErrInterrupted) {
			t.Errorf("%v: cause %v", first, cause)
		}
		if out.String() != "\nallocbench: stopping, press Ctrl-C again to quit at once\n" {
			t.Errorf("%v: output %q", first, out.String())
		}

		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-exits:
			if code != ExitInterrupted {
				t.Errorf("%v: exit code %d", first, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: the second signal did not exit", first)
		}
		stop()
	}
}

func TestInterruptContextStop(t *testing.T) {
	var out bytes.Buffer
	ctx, stop := interruptContext(&out, func(code int) { t.Errorf("exit %d", code) })
	if ctx.Err() != nil {
		t.Fatal("canceled at start")
	}
	stop()
	if !errors.Is(context.Cause(ctx), context.Canceled) || out.Len() != 0 {
		t.Errorf("cause %v, output %q", context.Cause(ctx), out.String())
	}
}

func TestPublicInterruptContext(t *testing.T) {
	ctx, stop := InterruptContext(&bytes.Buffer{})
	if ctx.Err() != nil {
		t.Error("canceled at start")
	}
	stop()
	if ctx.Err() == nil {
		t.Error("stop did not cancel")
	}
}
