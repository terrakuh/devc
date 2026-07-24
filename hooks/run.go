// Package hooks runs devcontainer lifecycle commands. It is transport-agnostic:
// the caller supplies an Executor that runs an argv (on the host for
// initializeCommand, inside the container for the rest), and hooks handles the
// three command shapes the spec allows - a shell string, an argv array, and an
// object whose entries run in parallel.
package hooks

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/terrakuh/devc/config"
)

// Executor runs a single resolved argv to completion, returning its error.
type Executor func(ctx context.Context, argv []string) error

// Run executes a lifecycle command. The string form runs via `/bin/sh -c`; the
// array form runs as a direct argv; the object form runs every entry
// concurrently and fails if any entry fails (reporting the first error).
func Run(ctx context.Context, cmd config.Command, exec Executor) error {
	switch cmd.Kind {
	case config.CommandNone:
		return nil
	case config.CommandShell, config.CommandArgv:
		return exec(ctx, toArgv(cmd))
	case config.CommandNamed:
		return runNamed(ctx, cmd.Named, exec)
	default:
		return fmt.Errorf("unknown command kind %v", cmd.Kind)
	}
}

// toArgv converts a Shell/Argv command to a concrete argv.
func toArgv(cmd config.Command) []string {
	switch cmd.Kind {
	case config.CommandShell:
		return []string{"/bin/sh", "-c", cmd.Shell}
	case config.CommandArgv:
		return cmd.Argv
	default:
		return nil
	}
}

// runNamed runs every named entry in parallel, in stable name order for
// deterministic scheduling, and returns the first error encountered.
func runNamed(ctx context.Context, named map[string]config.Command, exec Executor) error {
	names := make([]string, 0, len(named))
	for n := range named {
		names = append(names, n)
	}
	sort.Strings(names)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, name := range names {
		sub := named[name]
		wg.Add(1)
		go func(name string, sub config.Command) {
			defer wg.Done()
			if err := exec(ctx, toArgv(sub)); err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("hook %q: %w", name, err)
					cancel() // stop the siblings
				})
			}
		}(name, sub)
	}
	wg.Wait()
	return firstErr
}
