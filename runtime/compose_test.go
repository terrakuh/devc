package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidatesForPreference(t *testing.T) {
	pod := candidatesFor(string(Podman))
	assert.Equal(t, "podman compose", pod[0].label)

	dock := candidatesFor(string(Docker))
	assert.Equal(t, "docker compose", dock[0].label)
}

func TestDetectComposeOverrideMissing(t *testing.T) {
	_, err := DetectCompose(context.Background(), "podman", "definitely-not-a-real-compose-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found on PATH")
}

func TestComposeRunnerBasePrepended(t *testing.T) {
	// A "podman compose" implementation is a CLIRunner with Base=["compose"];
	// argv must be prefixed with the base on every call.
	r := &CLIRunner{Bin: "podman", Base: []string{"compose"}}
	got := r.argv([]string{"up", "--detach"})
	assert.Equal(t, []string{"compose", "up", "--detach"}, got)
	assert.Equal(t, "podman", r.Name())
}

// stubCompose puts executable stubs for the compose candidates on an isolated
// PATH (with an isolated cache dir and HOME) and returns a function reporting
// how many times each was probed. ok names the stubs whose `version` succeeds.
func stubCompose(t *testing.T, ok ...string) (bin string, probes func() []string) {
	t.Helper()
	bin = t.TempDir()
	log := filepath.Join(t.TempDir(), "probes")
	for _, name := range []string{"podman", "podman-compose", "docker", "docker-compose"} {
		status := "1"
		if slices.Contains(ok, name) {
			status = "0"
		}
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\nexit " + status + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755))
	}
	t.Setenv("PATH", bin)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("PODMAN_COMPOSE_PROVIDER", "")
	t.Setenv("DEVC_COMPOSE", "")
	return bin, func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func TestDetectComposeCachesProbe(t *testing.T) {
	bin, probes := stubCompose(t, "podman-compose")
	ctx := context.Background()

	c, err := DetectCompose(ctx, "podman", "")
	require.NoError(t, err)
	assert.Equal(t, "podman-compose", c.Label)
	assert.Equal(t, []string{"podman compose version", "podman-compose version"}, probes())

	// The second detection reuses the result without running anything.
	c, err = DetectCompose(ctx, "podman", "")
	require.NoError(t, err)
	assert.Equal(t, "podman-compose", c.Label)
	assert.Equal(t, filepath.Join(bin, "podman-compose"), c.Bin)
	assert.Len(t, probes(), 2)
}

func TestDetectComposeReprobesWhenBinariesChange(t *testing.T) {
	bin, probes := stubCompose(t, "podman-compose")
	ctx := context.Background()
	_, err := DetectCompose(ctx, "podman", "")
	require.NoError(t, err)

	// Upgrading podman (e.g. so `podman compose` now works) changes the binary.
	script := "#!/bin/sh\necho upgraded >/dev/null\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755))

	c, err := DetectCompose(ctx, "podman", "")
	require.NoError(t, err)
	assert.Equal(t, "podman compose", c.Label)
	assert.Len(t, probes(), 2, "the upgraded podman stub logs nothing; the old probes remain")
}

func TestDetectComposeReprobesWhenPluginInstalled(t *testing.T) {
	_, probes := stubCompose(t, "podman-compose")
	ctx := context.Background()
	_, err := DetectCompose(ctx, "docker", "")
	require.NoError(t, err)
	n := len(probes())

	plugins := filepath.Join(os.Getenv("HOME"), ".docker", "cli-plugins")
	require.NoError(t, os.MkdirAll(plugins, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plugins, "docker-compose"), []byte("x"), 0o755))

	_, err = DetectCompose(ctx, "docker", "")
	require.NoError(t, err)
	assert.Greater(t, len(probes()), n, "a new compose plugin invalidates the cache")
}

func TestDetectComposeDoesNotCacheFailure(t *testing.T) {
	_, probes := stubCompose(t)
	ctx := context.Background()
	_, err := DetectCompose(ctx, "podman", "")
	require.Error(t, err)
	_, err = DetectCompose(ctx, "podman", "")
	require.Error(t, err)
	assert.Len(t, probes(), 8, "every candidate is probed again")
}

func TestDetectComposeCacheIsPerRuntime(t *testing.T) {
	_, probes := stubCompose(t, "podman-compose", "docker-compose")
	ctx := context.Background()

	c, err := DetectCompose(ctx, "podman", "")
	require.NoError(t, err)
	assert.Equal(t, "podman-compose", c.Label)
	c, err = DetectCompose(ctx, "docker", "")
	require.NoError(t, err)
	assert.Equal(t, "docker-compose", c.Label, "docker prefers its own compose, not podman's cached pick")
	assert.Len(t, probes(), 4)
}
