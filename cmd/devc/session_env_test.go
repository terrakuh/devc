package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/terrakuh/devc/state"
)

// TestProbeIsFresh covers reuse of the cached env probe. Besides the container
// changing, an upgraded devc must re-probe: a cache written by an older version
// can hold variables this one deliberately no longer pins.
func TestProbeIsFresh(t *testing.T) {
	cached := &state.State{
		ContainerID:     "ctr-1",
		EnvProbe:        map[string]string{"PATH": "/usr/bin"},
		EnvProbeVersion: "1.0.0",
	}

	assert.True(t, probeIsFresh(cached, "ctr-1", "1.0.0"))
	assert.False(t, probeIsFresh(cached, "ctr-2", "1.0.0"), "a new container must re-probe")
	assert.False(t, probeIsFresh(cached, "ctr-1", "1.1.0"), "a devc upgrade must re-probe")

	assert.False(t, probeIsFresh(&state.State{ContainerID: "ctr-1"}, "ctr-1", "1.0.0"),
		"no cached probe")
	assert.False(t, probeIsFresh(&state.State{
		ContainerID: "ctr-1", EnvProbe: map[string]string{"PATH": "/usr/bin"},
	}, "ctr-1", "1.0.0"), "a cache from before versioning must re-probe")
}
