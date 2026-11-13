package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/SolonenkoNikita/allocbench/internal/proc"
)

const ExitInterrupted = 130

func InterruptContext(w io.Writer) (context.Context, func()) {
	return interruptContext(w, os.Exit)
}

func interruptContext(w io.Writer, exit func(int)) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
		case <-done:
			return
		}
		fmt.Fprintln(w, "\nallocbench: stopping, press Ctrl-C again to quit at once")
		cancel(proc.ErrInterrupted)
		select {
		case <-signals:
			exit(ExitInterrupted)
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		close(done)
		cancel(nil)
	}
}
