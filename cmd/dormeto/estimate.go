package main

// `dormeto estimate`: the Node estimate's command, its arguments passed as they are to cli.Main (the command parses
// its own options, as the Node one) with the real dependencies — the clock, the cache directory, stdout and stderr,
// each profile's AWS over the AWS SDK for Go v2, the terminal's progress line when stdout is one, the build's version
// for the JSON report — and its exit code returned: 0; 1 when AWS refuses; 2 on a bad option; 128 and the signal's
// number when SIGINT (Ctrl-C) or SIGTERM stopped it.
//
// Ports manager packages/estimate/src/bin.ts (main's call, process.exit).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/cli"
	"github.com/Dormeto/cli/internal/options"
)

// exitCode is a command's exit code other than 0: main exits with it, cobra says nothing of it.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit code %d", int(e)) }

// execute runs the command line and gives the process's exit code.
func execute(root *cobra.Command) int {
	err := root.Execute()
	var code exitCode
	switch {
	case errors.As(err, &code):
		return int(code)
	case err != nil:
		return 1
	}
	return 0
}

func newEstimate(b build) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate --profile <profile>[,<profile>...] [options]",
		Short: "Estimate what Dormeto would save on AWS accounts (read-only)",
		// The options are the command's, checked and said as the Node estimate's; --help prints its usage.
		DisableFlagParsing: true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, signalled, stop := interruptible(cmd.Context())
			defer stop()
			code := cli.Main(args, dependencies(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), b))
			if s := signalled(); s != nil {
				code = signalCode(s)
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) { fmt.Fprintln(c.OutOrStdout(), options.Usage) })
	return cmd
}

// dependencies are the command's real ones (bin.ts's): the clock, the cache directory, out and errs a line at a
// time, each profile's AWS, the terminal's Status when out is a terminal, the build's version.
func dependencies(ctx context.Context, out, errs io.Writer, b build) cli.Deps {
	version, _, _ := b.resolve()
	return cli.Deps{
		Context: ctx, Now: time.Now, CacheRoot: cache.Dir(os.Getenv),
		Log:   func(line string) { fmt.Fprintln(out, line) },
		Error: func(line string) { fmt.Fprintln(errs, line) },
		AWS:   cli.AWSOf, Status: cli.StatusOf(out), Version: orDefault(version, "dev"),
	}
}

// interruptible is parent cancelled by the first SIGINT (Ctrl-C) or SIGTERM (a kill, the npm launcher passing one
// on), and the signal that came (nil: none). Once one came, the next has its default effect: the process is killed
// at once. stop releases the signals and the context.
func interruptible(parent context.Context) (ctx context.Context, signalled func() os.Signal, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	var (
		mu   sync.Mutex
		got  os.Signal
		done = make(chan struct{})
		once sync.Once
	)
	go func() {
		select {
		case s := <-signals:
			mu.Lock()
			got = s
			mu.Unlock()
			signal.Stop(signals)
			cancel()
		case <-done:
		}
	}()
	signalled = func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	stop = func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
			cancel()
		})
	}
	return ctx, signalled, stop
}

// signalCode is the exit code of a run a signal stopped: 128 and its number (130 for Ctrl-C), what a shell says of a
// process the signal killed, as the Node estimate is (bin.ts sets no handler).
func signalCode(s os.Signal) int {
	if n, ok := s.(syscall.Signal); ok {
		return 128 + int(n)
	}
	return cli.Interrupted
}
