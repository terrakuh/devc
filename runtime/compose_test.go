package runtime

import (
	"context"
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
