package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/hooks"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/state"
)

// hookCtx carries what the lifecycle hooks need for one `up`.
type hookCtx struct {
	e           *env
	containerID string // the running container's id (once-semantics key)
	ref         string // exec reference (name or id)
	skipHooks   bool
	rerunHooks  bool
}

// hostExecutor runs a hook argv on the host (for initializeCommand), in the
// local workspace folder.
func (h *hookCtx) hostExecutor() hooks.Executor {
	return func(ctx context.Context, argv []string) error {
		if len(argv) == 0 {
			return nil
		}
		return runHostArgv(ctx, argv, h.e.spec.LocalWorkspaceFolder)
	}
}

// containerExecutor runs a hook argv inside the container as the remote user, in
// the workspace folder, with the resolved session environment.
func (h *hookCtx) containerExecutor() hooks.Executor {
	io := runtime.IO{Stdout: os.Stderr, Stderr: os.Stderr} // hook output is diagnostic
	return func(ctx context.Context, argv []string) error {
		full := container.ExecArgs(h.ref, h.e.spec.RemoteUser, h.e.spec.ContainerWorkspaceFolder, h.e.effectiveEnv(), true, false, argv)
		return h.e.runner.Run(ctx, full, io)
	}
}

// runInitialize runs initializeCommand on the host. It runs on every `up`,
// before the container is created.
func runInitialize(ctx context.Context, e *env) error {
	if e.spec.Hooks.Initialize.IsZero() {
		return nil
	}
	fmt.Fprintf(os.Stderr, "devc: running initializeCommand...\n")
	h := &hookCtx{e: e}
	return hooks.Run(ctx, e.spec.Hooks.Initialize, h.hostExecutor())
}

// runLifecycleHooks runs the in-container hooks in order, honoring once-per-
// container-identity semantics for the create hooks. It is called after the
// container is up and before the workspace is reported ready.
func (h *hookCtx) runLifecycleHooks(ctx context.Context) error {
	if h.skipHooks {
		return nil
	}
	dir, err := state.For(h.e.spec.ID)
	if err != nil {
		return err
	}
	st, err := dir.Load()
	if err != nil {
		return err
	}

	// A new container identity resets the create-hook markers (and the cached
	// env probe) so both re-run against the fresh container.
	if st.ContainerID != h.containerID || h.rerunHooks {
		st.Hooks = nil
		if st.ContainerID != h.containerID {
			st.EnvProbe = nil
		}
	}
	if st.Hooks == nil {
		st.Hooks = map[string]string{}
	}
	// Record the identity these markers belong to, so the next `up` recognizes
	// the same container and skips the create hooks.
	st.ContainerID = h.containerID
	st.ID = h.e.spec.ID

	exec := h.containerExecutor()

	// Create hooks: once per container identity.
	createHooks := []struct {
		name string
		cmd  config.Command
	}{
		{string(config.HookOnCreate), h.e.spec.Hooks.OnCreate},
		{string(config.HookUpdateContent), h.e.spec.Hooks.UpdateContent},
		{string(config.HookPostCreate), h.e.spec.Hooks.PostCreate},
	}
	for _, hook := range createHooks {
		if hook.cmd.IsZero() || st.Hooks[hook.name] != "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "devc: running %s...\n", hook.name)
		if err := hooks.Run(ctx, hook.cmd, exec); err != nil {
			return fmt.Errorf("%s: %w", hook.name, err)
		}
		st.Hooks[hook.name] = time.Now().UTC().Format(time.RFC3339)
		_ = dir.Save(st) // persist progress after each hook
	}

	// postStart runs on every start.
	if !h.e.spec.Hooks.PostStart.IsZero() {
		fmt.Fprintf(os.Stderr, "devc: running postStartCommand...\n")
		if err := hooks.Run(ctx, h.e.spec.Hooks.PostStart, exec); err != nil {
			return fmt.Errorf("postStartCommand: %w", err)
		}
	}
	return dir.Save(st)
}

// runPostAttach runs postAttachCommand. It runs on every `up` (and, later, on
// interactive ssh - a documented deviation is that it does not run on VSCodium's
// exec channels).
func (h *hookCtx) runPostAttach(ctx context.Context) error {
	if h.skipHooks || h.e.spec.Hooks.PostAttach.IsZero() {
		return nil
	}
	fmt.Fprintf(os.Stderr, "devc: running postAttachCommand...\n")
	return hooks.Run(ctx, h.e.spec.Hooks.PostAttach, h.containerExecutor())
}
