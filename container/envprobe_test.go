package container

import (
	"context"
	"testing"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeShellFlags(t *testing.T) {
	assert.Equal(t, "-l", probeShellFlags(config.EnvProbeLogin))
	assert.Equal(t, "-i", probeShellFlags(config.EnvProbeInteractive))
	assert.Equal(t, "-li", probeShellFlags(config.EnvProbeLoginInteractive))
	assert.Equal(t, "", probeShellFlags(config.EnvProbeNone))
}

func TestParseEnvOutput(t *testing.T) {
	out := "PATH=/usr/local/bin:/usr/bin\nHOME=/root\n_=whatever\nSHLVL=1\nBAD_LINE\nGOPATH=/go\n"
	env := parseEnvOutput(out)
	assert.Equal(t, "/usr/local/bin:/usr/bin", env["PATH"])
	assert.Equal(t, "/go", env["GOPATH"])
	assert.NotContains(t, env, "_", "process-specific vars are dropped")
	assert.NotContains(t, env, "SHLVL")
	assert.NotContains(t, env, "BAD_LINE")
}

func TestProbeEnvNoneSkips(t *testing.T) {
	f := runtime.NewFake()
	got := ProbeEnv(context.Background(), f, "ctr", "root", config.EnvProbeNone)
	assert.Nil(t, got)
	assert.Empty(t, f.Calls, "EnvProbeNone must not exec anything")
}

func TestProbeEnvRunsAndParses(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		// The probe execs a login+interactive shell running `env`.
		assert.Contains(t, args, "-li")
		assert.Contains(t, args, "env")
		return []byte("PATH=/opt/bin\nFOO=bar\n"), nil
	}
	got := ProbeEnv(context.Background(), f, "ctr", "vscode", config.EnvProbeLoginInteractive)
	require.NotNil(t, got)
	assert.Equal(t, "/opt/bin", got["PATH"])
	assert.Equal(t, "bar", got["FOO"])
}

func TestProbeEnvToleratesFailure(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		return nil, assert.AnError
	}
	got := ProbeEnv(context.Background(), f, "ctr", "root", config.EnvProbeLogin)
	assert.Nil(t, got, "probe failure must be silent (nil), never fatal")
}
