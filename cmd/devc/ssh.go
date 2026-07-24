package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// runSSH implements `devc ssh`:
//
//	devc ssh [--path .]                 -> spawn `ssh devc.<name>` (shares ControlMaster)
//	devc ssh --stdio [--start] [--path] -> ProxyCommand mode: pipe stdio to the agent
//
// The --stdio form is what the generated ssh config's ProxyCommand runs; it
// resolves the workspace's container and execs the injected agent over a pipe.
func runSSH(args []string) error {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	stdio := fs.Bool("stdio", false, "ProxyCommand mode: bridge stdin/stdout to the agent")
	start := fs.Bool("start", false, "ensure the workspace is up before connecting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// A trailing positional workspace name is accepted (the ProxyCommand passes
	// it) but --path is the source of truth; the name is informational.
	ctx := context.Background()

	// The --stdio path is the ProxyCommand transport for GUI editors, which
	// discard its stderr and surface only "connection lost before handshake".
	// Wrap the whole thing - including setup(), the most likely fast-failure
	// point (PATH/config resolution) - in a debug log the user can inspect.
	if *stdio {
		dbg := newProxyLog()
		var runErr error
		defer func() { dbg.finish(runErr) }()
		dbg.logf("stdio invoked: args=%v", args)

		e, err := setup(ctx, &cf)
		if err != nil {
			dbg.logf("setup failed: %v", err)
			runErr = err
			return err
		}
		runErr = sshStdio(ctx, e, *start, dbg)
		return runErr
	}

	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}
	return sshSpawn(e)
}

// sshStdio bridges the current process's stdio to the in-container agent. This
// is the transport under the ssh ProxyCommand. Steps are mirrored to dbg so a
// failure invisible to the editor can be diagnosed after the fact.
func sshStdio(ctx context.Context, e *env, start bool, dbg *proxyLog) (err error) {
	dbg.logf("stdio start: workspace=%s runtime=%s start=%v", e.spec.Name, e.runner.Name(), start)

	ref, info, err := e.attachRef(ctx)
	if err != nil {
		return err
	}
	if info == nil || !info.Running() {
		if !start {
			return fmt.Errorf("workspace %q is not running (retry with --start or run `devc up`)", e.spec.Name)
		}
		dbg.logf("workspace not running; bringing it up")
		if err := ensureUp(ctx, e); err != nil {
			return err
		}
		if ref, info, err = e.attachRef(ctx); err != nil {
			return err
		}
		if info == nil || !info.Running() {
			return fmt.Errorf("workspace %q did not come up", e.spec.Name)
		}
	}

	argv := container.AgentServeArgs(ref, e.spec.RemoteUser, e.spec.ContainerWorkspaceFolder)
	dbg.logf("exec: %s %s", e.runner.Name(), strings.Join(argv, " "))
	io := runtime.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	return e.runner.Run(ctx, argv, io)
}

// sshSpawn execs the system ssh client against the workspace alias so the CLI,
// VSCodium, and ControlMaster all share one connection path.
func sshSpawn(e *env) error {
	alias := sshAlias(e.spec)
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	cmd := exec.Command(bin, alias)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ensureUp brings the workspace up (single or compose) without printing the
// usual up banner noise to stdout - ProxyCommand stdout is the ssh transport,
// so status goes to stderr.
func ensureUp(ctx context.Context, e *env) error {
	// Route human output to stderr; stdout must stay clean for the ssh pipe.
	stdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = stdout }()

	if e.spec.Kind == config.KindCompose {
		return upCompose(ctx, e, hookFlags{})
	}
	return upSingle(ctx, e, false, false, hookFlags{})
}

// sshAlias is the ssh Host name for a workspace, "devc.<name>".
func sshAlias(spec *config.Spec) string { return "devc." + spec.Name }
