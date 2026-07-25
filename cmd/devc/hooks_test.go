package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
)

func shell(s string) config.Command { return config.Command{Kind: config.CommandShell, Shell: s} }

// countExecOf returns how many recorded exec calls end with the given command
// string (the last argv element of a `/bin/sh -c <cmd>` invocation).
func countExecOf(f *runtime.FakeRunner, cmd string) int {
	n := 0
	for _, call := range f.Calls {
		if len(call) > 0 && call[len(call)-1] == cmd {
			n++
		}
	}
	return n
}

func newHookEnv(f *runtime.FakeRunner, hooks config.Hooks) *env {
	return &env{
		spec: &config.Spec{
			ID:                       "w-1a2b3c4d",
			Name:                     "w",
			Kind:                     config.KindImage,
			ContainerWorkspaceFolder: "/w",
			Hooks:                    hooks,
		},
		runner: f,
		flags:  &commonFlags{},
	}
}

func TestOnceSemanticsCreateHooks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	f := runtime.NewFake()
	e := newHookEnv(f, config.Hooks{
		OnCreate:  shell("echo create"),
		PostStart: shell("echo start"),
	})

	h := &hookCtx{e: e, containerID: "cid-1", ref: "w-1a2b3c4d"}
	require.NoError(t, h.runLifecycleHooks(ctx))
	require.NoError(t, h.runLifecycleHooks(ctx)) // second "up", same container

	assert.Equal(t, 1, countExecOf(f, "echo create"), "onCreate runs once per container identity")
	assert.Equal(t, 2, countExecOf(f, "echo start"), "postStart runs on every start")
}

func TestCreateHooksRerunOnNewContainer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	f := runtime.NewFake()
	e := newHookEnv(f, config.Hooks{OnCreate: shell("echo create")})

	h1 := &hookCtx{e: e, containerID: "cid-1", ref: "w"}
	require.NoError(t, h1.runLifecycleHooks(ctx))

	// New container identity (e.g. after --recreate) re-runs create hooks.
	h2 := &hookCtx{e: e, containerID: "cid-2", ref: "w"}
	require.NoError(t, h2.runLifecycleHooks(ctx))

	assert.Equal(t, 2, countExecOf(f, "echo create"), "recreated container re-runs onCreate")
}

func TestRerunHooksFlagForcesRerun(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	f := runtime.NewFake()
	e := newHookEnv(f, config.Hooks{OnCreate: shell("echo create")})

	h := &hookCtx{e: e, containerID: "cid-1", ref: "w"}
	require.NoError(t, h.runLifecycleHooks(ctx))
	h.rerunHooks = true
	require.NoError(t, h.runLifecycleHooks(ctx))

	assert.Equal(t, 2, countExecOf(f, "echo create"), "--rerun-hooks forces create hooks again")
}

func TestSkipHooks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	f := runtime.NewFake()
	e := newHookEnv(f, config.Hooks{OnCreate: shell("echo create"), PostStart: shell("echo start")})

	h := &hookCtx{e: e, containerID: "cid-1", ref: "w", skipHooks: true}
	require.NoError(t, h.runLifecycleHooks(ctx))
	assert.Empty(t, f.Calls, "--skip-hooks runs nothing")
}
