package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// runExec implements `devc exec [flags] -- <cmd>...`, running a command inside
// the workspace container as the remote user, in the workspace folder, with the
// configured remoteEnv. A TTY is allocated when stdin is a terminal (unless
// -T disables it), so both `devc exec -- bash` and piped use work.
func runExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	noTTY := fs.Bool("T", false, "disable TTY allocation even when stdin is a terminal")
	service := fs.String("service", "", "compose: run in this service's container instead of the workspace's")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		return fmt.Errorf("usage: devc exec [flags] -- <command> [args...]")
	}

	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	ref, info, err := e.serviceRef(ctx, *service)
	if err != nil {
		return err
	}
	if info == nil || !info.Running() {
		if other := *service; other != "" && other != e.spec.Compose.Service {
			return fmt.Errorf("service %q is not running; run `devc up %s` first", other, other)
		}
		return fmt.Errorf("workspace %q is not running; run `devc up` first", e.spec.Name)
	}

	// remoteUser, the workspace folder and remoteEnv describe the workspace's own
	// container; another service has its own user and layout, so the command runs
	// there plainly, letting the image's defaults apply.
	user, workdir, env := e.spec.RemoteUser, e.spec.ContainerWorkspaceFolder, e.spec.RemoteEnv
	if *service != "" && *service != e.spec.Compose.Service {
		user, workdir, env = "", "", nil
	}

	tty := !*noTTY && stdinIsTerminal()
	argv := container.ExecArgs(ref, user, workdir, env, true, tty, cmdArgs)
	io := runtime.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	return e.runner.Run(ctx, argv, io)
}

// stdinIsTerminal reports whether stdin is a character device, a dependency-free
// heuristic for "interactive" that is good enough to decide TTY allocation.
// The SSH agent does its own PTY handling and never relies on this.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
