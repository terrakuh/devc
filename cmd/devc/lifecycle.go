package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/sshconf"
	"github.com/terrakuh/devc/state"
)

func runDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	volumes := fs.Bool("volumes", false, "also remove named volumes (compose: down --volumes)")
	purge := fs.Bool("purge", false, "also delete the workspace's host-side state (keys, hooks)")
	auto := fs.Bool("auto", false, "honor the config's shutdownAction (no-op when it is 'none')")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	// --auto honors shutdownAction (used on editor disconnect): 'none' leaves the
	// workspace running, and the 'stop' actions stop it without removing it - only
	// an explicit `devc down` (no --auto) tears the container down. Removing a
	// container the config only asked to stop would discard its filesystem state.
	if *auto {
		switch e.spec.ShutdownAction {
		case config.ShutdownNone:
			fmt.Printf("workspace %q has shutdownAction=none; leaving it running\n", e.spec.Name)
			return nil
		case config.ShutdownStopCtr, config.ShutdownStopCompose:
			return stopWorkspace(ctx, e)
		}
	}

	if e.spec.Kind == config.KindCompose {
		comp, err := e.composeImpl(ctx)
		if err != nil {
			return err
		}
		project := container.ProjectName(e.spec)
		fmt.Printf("tearing down compose project %q...\n", project)
		io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}
		if err := container.ComposeDown(ctx, comp, e.spec, project, *volumes, io); err != nil {
			return err
		}
	} else {
		name := container.ContainerName(e.spec)
		existing, err := container.Find(ctx, e.runner, name)
		if err != nil {
			return err
		}
		if existing == nil {
			fmt.Printf("workspace %q is not created\n", e.spec.Name)
		} else {
			fmt.Printf("removing %q...\n", e.spec.Name)
			if err := container.Remove(ctx, e.runner, name); err != nil {
				return err
			}
		}
	}

	if *purge {
		if dir, err := state.For(e.spec.ID); err == nil {
			if err := dir.Remove(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not remove state dir: %v\n", err)
			}
		}
		if err := removeControlDir(e.spec.ID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove control socket dir: %v\n", err)
		}
		if err := sshconf.RemoveWorkspaceConfig(devcSSHConfigPath(e.spec.ID)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove ssh config: %v\n", err)
		}
	}
	return nil
}

func runStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}
	return stopWorkspace(ctx, e)
}

// stopWorkspace stops the workspace without removing it (single container or
// compose). Shared by `devc stop` and `devc down --auto` under a 'stop'
// shutdownAction.
func stopWorkspace(ctx context.Context, e *env) error {
	if e.spec.Kind == config.KindCompose {
		comp, err := e.composeImpl(ctx)
		if err != nil {
			return err
		}
		project := container.ProjectName(e.spec)
		fmt.Printf("stopping compose project %q...\n", project)
		io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}
		return comp.Run(ctx, container.ComposeStopArgs(e.spec, project), io)
	}

	name := container.ContainerName(e.spec)
	existing, err := container.Find(ctx, e.runner, name)
	if err != nil {
		return err
	}
	if existing == nil || !existing.Running() {
		fmt.Printf("workspace %q is not running\n", e.spec.Name)
		return nil
	}
	fmt.Printf("stopping %q...\n", e.spec.Name)
	return container.Stop(ctx, e.runner, name)
}

func runRestart(args []string) error {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	all := fs.Bool("all", false, "compose: restart all of the workspace's services, not just the main one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	if e.spec.Kind == config.KindCompose {
		comp, err := e.composeImpl(ctx)
		if err != nil {
			return err
		}
		project := container.ProjectName(e.spec)
		if *all {
			fmt.Printf("restarting compose project %q...\n", project)
		} else {
			fmt.Printf("restarting service %q in compose project %q...\n", e.spec.Compose.Service, project)
		}
		io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}
		return comp.Run(ctx, container.ComposeRestartArgs(e.spec, project, *all), io)
	}

	if *all {
		fmt.Fprintln(os.Stderr, "warning: --all only applies to compose workspaces; restarting the container")
	}
	name := container.ContainerName(e.spec)
	existing, err := container.Find(ctx, e.runner, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("workspace %q is not created", e.spec.Name)
	}
	fmt.Printf("restarting %q...\n", e.spec.Name)
	return container.Restart(ctx, e.runner, name)
}

// statusReport is the machine-readable form of `devc status` (--json).
type statusReport struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Runtime     string `json:"runtime"`
	Local       string `json:"localWorkspaceFolder"`
	Container   string `json:"containerWorkspaceFolder"`
	State       string `json:"state"`
	ContainerID string `json:"containerID,omitempty"`
	ConfigDrift bool   `json:"configDrift"`
	Ports       []int  `json:"forwardPorts,omitempty"`
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	jsonOut := fs.Bool("json", false, "emit the status as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	_, info, err := e.attachRef(ctx)
	if err != nil {
		return err
	}

	rep := statusReport{
		Name:      e.spec.Name,
		ID:        e.spec.ID,
		Kind:      string(e.spec.Kind),
		Runtime:   e.runner.Name(),
		Local:     e.spec.LocalWorkspaceFolder,
		Container: e.spec.ContainerWorkspaceFolder,
		State:     "not created",
	}
	if info != nil {
		rep.ContainerID = short(info.ID)
		rep.State = containerState(info)
		rep.ConfigDrift = info.Config.Labels[container.LabelConfigHash] != "" &&
			info.Config.Labels[container.LabelConfigHash] != container.ConfigHash(e.spec)
	}
	for _, p := range e.spec.Ports {
		rep.Ports = append(rep.Ports, p.HostPort)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Printf("workspace:  %s (%s)\n", rep.Name, rep.ID)
	fmt.Printf("kind:       %s\n", rep.Kind)
	fmt.Printf("runtime:    %s\n", rep.Runtime)
	fmt.Printf("folder:     %s -> %s\n", rep.Local, rep.Container)
	if e.spec.Kind == config.KindCompose {
		fmt.Printf("compose:    project %s, service %s\n", container.ProjectName(e.spec), e.spec.Compose.Service)
	}
	if rep.ContainerID == "" {
		fmt.Printf("state:      %s\n", rep.State)
	} else {
		fmt.Printf("state:      %s (%s)\n", rep.State, rep.ContainerID)
	}
	if rep.ConfigDrift {
		fmt.Printf("            config changed since creation - `devc up --recreate` to apply\n")
	}
	for _, p := range e.spec.Ports {
		fmt.Printf("port:       %d -> container %d\n", p.HostPort, p.ContainerPort)
	}
	return nil
}

func runLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	follow := fs.Bool("follow", false, "stream new log output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	service := ""
	if fs.NArg() > 0 {
		service = fs.Arg(0)
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}
	io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}

	if e.spec.Kind == config.KindCompose {
		comp, err := e.composeImpl(ctx)
		if err != nil {
			return err
		}
		project := container.ProjectName(e.spec)
		return comp.Run(ctx, container.ComposeLogsArgs(e.spec, project, *follow, service), io)
	}

	// Single container: logs come from the runtime directly.
	ref, info, err := e.attachRef(ctx)
	if err != nil {
		return err
	}
	if info == nil {
		return fmt.Errorf("workspace %q is not created", e.spec.Name)
	}
	logArgs := []string{"logs"}
	if *follow {
		logArgs = append(logArgs, "--follow")
	}
	logArgs = append(logArgs, ref)
	return e.runner.Run(ctx, logArgs, io)
}
