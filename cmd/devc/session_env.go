package main

import (
	"context"
	"maps"
	"os"
	"os/exec"

	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/state"
)

// runHostArgv runs a hook argv on the host in dir, wiring output to stderr (so
// stdout stays clean for callers that stream the ssh transport).
func runHostArgv(ctx context.Context, argv []string, dir string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

// resolveSessionEnv computes the environment injected into ssh sessions and the
// agent env file: the userEnvProbe result as a base, overlaid by the explicit
// remoteEnv (config wins). The probe is cached in workspace state and refreshed
// when the container identity changes.
func resolveSessionEnv(ctx context.Context, e *env, ref, containerID string) map[string]string {
	merged := map[string]string{}

	probe := loadOrRunProbe(ctx, e, ref, containerID)
	maps.Copy(merged, probe)
	// remoteEnv takes precedence over probed values.
	maps.Copy(merged, e.spec.RemoteEnv)
	return merged
}

// probeIsFresh reports whether a cached probe can be reused: same container, and
// written by this version of devc.
func probeIsFresh(st *state.State, containerID, devcVersion string) bool {
	return st.EnvProbe != nil && st.ContainerID == containerID && st.EnvProbeVersion == devcVersion
}

// loadOrRunProbe returns the cached env probe for the current container, running
// it if absent. Failures are silent (probe is best-effort).
func loadOrRunProbe(ctx context.Context, e *env, ref, containerID string) map[string]string {
	dir, err := state.For(e.spec.ID)
	if err != nil {
		return nil
	}
	st, err := dir.Load()
	if err != nil {
		return nil
	}
	if probeIsFresh(st, containerID, version) {
		return st.EnvProbe
	}
	probe := container.ProbeEnv(ctx, e.runner, ref, e.spec.RemoteUser, e.spec.EnvProbe)
	st.EnvProbe = probe
	st.EnvProbeVersion = version
	st.ContainerID = containerID
	_ = dir.Save(st)
	return probe
}
