package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/state"
)

// hookFlags carries the hook-related `up` options.
type hookFlags struct {
	skip  bool
	rerun bool
}

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	recreate := fs.Bool("recreate", false, "remove and recreate the container even if it exists")
	rebuild := fs.Bool("rebuild", false, "rebuild the image before (re)creating the container")
	hf := hookFlags{}
	fs.BoolVar(&hf.skip, "skip-hooks", false, "do not run lifecycle hooks")
	fs.BoolVar(&hf.rerun, "rerun-hooks", false, "re-run create hooks even if already run for this container")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	// initializeCommand runs on the host, before any container work.
	if !hf.skip {
		if err := runInitialize(ctx, e); err != nil {
			return fmt.Errorf("initializeCommand: %w", err)
		}
	}

	if e.spec.Kind == config.KindCompose {
		return upCompose(ctx, e, hf)
	}
	return upSingle(ctx, e, *recreate, *rebuild, hf)
}

// postUp runs the in-container lifecycle: create/start hooks, environment probe,
// agent provisioning, and postAttach. This sequence is shared by both bring-up
// kinds once a container is running. ref is the exec reference, containerID the
// once-semantics key.
func postUp(ctx context.Context, e *env, ref, containerID string, hf hookFlags) error {
	h := &hookCtx{e: e, containerID: containerID, ref: ref, skipHooks: hf.skip, rerunHooks: hf.rerun}

	// Create/start hooks run before we probe env, so postCreate's changes to the
	// shell profile are reflected in the probe.
	if err := h.runLifecycleHooks(ctx); err != nil {
		return err
	}
	e.sessionEnv = resolveSessionEnv(ctx, e, ref, containerID)

	if err := provision(ctx, e, ref); err != nil {
		return err
	}
	return h.runPostAttach(ctx)
}

// upCompose brings the workspace's compose project up (detached) and locates
// the attach service.
func upCompose(ctx context.Context, e *env, hf hookFlags) error {
	comp, err := e.composeImpl(ctx)
	if err != nil {
		return err
	}
	project := container.ProjectName(e.spec)
	io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}

	fmt.Printf("bringing up compose project %q (%s)...\n", project, comp.Label)
	if err := container.ComposeUp(ctx, comp, e.spec, project, io); err != nil {
		return err
	}

	info, err := container.FindComposeService(ctx, e.runner, project, e.spec.Compose.Service)
	if err != nil {
		return err
	}
	if info == nil {
		return fmt.Errorf("compose came up but service %q has no container; check the compose file", e.spec.Compose.Service)
	}
	if err := persistCompose(e, project); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write workspace state: %v\n", err)
	}
	if err := postUp(ctx, e, info.ID, info.ID, hf); err != nil {
		return err
	}
	fmt.Printf("workspace %q is up (service %q, container %s)\n", e.spec.Name, e.spec.Compose.Service, short(info.ID))
	fmt.Printf("  ssh in:         ssh %s\n", sshAlias(e.spec))
	fmt.Printf("  run a command:  devc exec --path %s -- <cmd>\n", e.spec.LocalWorkspaceFolder)
	return nil
}

func persistCompose(e *env, project string) error {
	dir, err := state.For(e.spec.ID)
	if err != nil {
		return err
	}
	s, err := dir.Load()
	if err != nil {
		return err
	}
	s.ID = e.spec.ID
	s.Name = e.spec.Name
	s.LocalFolder = e.spec.LocalWorkspaceFolder
	s.ConfigHash = container.ConfigHash(e.spec)
	s.ComposeProject = project
	// s.ContainerID is owned by the hook/probe logic; see persist().
	return dir.Save(s)
}

// upSingle brings a single-container workspace to running state.
func upSingle(ctx context.Context, e *env, recreate, rebuild bool, hf hookFlags) error {
	name := container.ContainerName(e.spec)
	io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}

	existing, err := container.Find(ctx, e.runner, name)
	if err != nil {
		return err
	}
	action := container.Decide(existing, e.spec, recreate)

	switch action {
	case container.ActionAttach:
		fmt.Printf("workspace %q already running\n", e.spec.Name)
	case container.ActionStart:
		fmt.Printf("starting %q...\n", e.spec.Name)
		if err := container.Start(ctx, e.runner, name); err != nil {
			return err
		}
	case container.ActionDrift:
		fmt.Fprintf(os.Stderr, "warning: devcontainer.json changed since this container was created; run `devc up --recreate` to apply. Using the existing container.\n")
		if existing != nil && !existing.Running() {
			if err := container.Start(ctx, e.runner, name); err != nil {
				return err
			}
		}
	case container.ActionRecreate:
		fmt.Printf("recreating %q...\n", e.spec.Name)
		if err := container.Remove(ctx, e.runner, name); err != nil {
			return err
		}
		if err := createFresh(ctx, e, io, rebuild); err != nil {
			return err
		}
	case container.ActionCreate:
		if err := createFresh(ctx, e, io, rebuild); err != nil {
			return err
		}
	}

	// Record the workspace config hash for drift detection.
	if err := persist(e); err != nil {
		// Non-fatal: the container is up regardless of state bookkeeping.
		fmt.Fprintf(os.Stderr, "warning: could not write workspace state: %v\n", err)
	}

	// Resolve the container id (once-semantics key for hooks).
	containerID := name
	if info, err := container.Find(ctx, e.runner, name); err == nil && info != nil {
		containerID = info.ID
	}
	if err := postUp(ctx, e, name, containerID, hf); err != nil {
		return err
	}

	fmt.Printf("workspace %q is up (%s)\n", e.spec.Name, name)
	fmt.Printf("  ssh in:         ssh %s\n", sshAlias(e.spec))
	fmt.Printf("  run a command:  devc exec --path %s -- <cmd>\n", e.spec.LocalWorkspaceFolder)
	return nil
}

// createFresh builds (when needed) and runs a new container.
func createFresh(ctx context.Context, e *env, io runtime.IO, rebuild bool) error {
	if e.spec.Kind == config.KindBuild {
		if rebuild {
			fmt.Printf("building image for %q...\n", e.spec.Name)
		}
		if err := container.Build(ctx, e.runner, e.spec, e.flags.platform, io); err != nil {
			return err
		}
	}
	fmt.Printf("creating %q...\n", e.spec.Name)
	id, err := container.Create(ctx, e.runner, e.spec, e.create, e.flags.platform)
	if err != nil {
		return err
	}
	fmt.Printf("  container %s\n", short(id))
	return nil
}

// persist records the workspace's config hash. The container identity
// (s.ContainerID) is deliberately owned by the hook/probe logic (its
// once-semantics key), not written here; persist would otherwise clobber the
// identity change that triggers create-hook re-runs on `--recreate`.
func persist(e *env) error {
	dir, err := state.For(e.spec.ID)
	if err != nil {
		return err
	}
	s, err := dir.Load()
	if err != nil {
		return err
	}
	s.ID = e.spec.ID
	s.Name = e.spec.Name
	s.LocalFolder = e.spec.LocalWorkspaceFolder
	s.ConfigHash = container.ConfigHash(e.spec)
	return dir.Save(s)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
