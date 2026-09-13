package container

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
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

// TestParseEnvOutputDropsSessionScoped covers variables that describe the probe
// shell's own session rather than the container. Pinning them into every later
// session bakes in a value that is wrong or dangling by then - XDG_RUNTIME_DIR
// names a directory nothing creates in a container, and the VSCodium server
// install script uses it as its lock and temp dir.
func TestParseEnvOutputDropsSessionScoped(t *testing.T) {
	out := strings.Join([]string{
		"PATH=/usr/bin",
		"XDG_RUNTIME_DIR=/run/user/0",
		"SSH_AUTH_SOCK=/tmp/ssh-XXXX/agent.42",
		"SSH_CONNECTION=10.0.0.1 55000 10.0.0.2 22",
		"SSH_CLIENT=10.0.0.1 55000 22",
		"SSH_TTY=/dev/pts/3",
		"GPG_TTY=not a tty",
		"TERM=dumb",
		"",
	}, "\n")

	env := parseEnvOutput(out)
	assert.Equal(t, "/usr/bin", env["PATH"], "real container env still comes through")
	for _, k := range []string{
		"XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "SSH_CONNECTION",
		"SSH_CLIENT", "SSH_TTY", "GPG_TTY", "TERM",
	} {
		assert.NotContains(t, env, k, "%s belongs to the probe shell, not the container", k)
	}
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
