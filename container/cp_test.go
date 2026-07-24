package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/runtime"
)

func TestNormalizeArch(t *testing.T) {
	assert.Equal(t, "amd64", normalizeArch("x86_64"))
	assert.Equal(t, "arm64", normalizeArch("aarch64"))
	assert.Equal(t, "arm", normalizeArch("armv7l"))
	assert.Equal(t, "riscv64", normalizeArch("riscv64"))
}

func TestAgentServeArgs(t *testing.T) {
	args := AgentServeArgs("ctr-1", "vscode", "/workspace", false)
	assert.Equal(t, "exec", args[0])
	assert.NotContains(t, args, "--forward-agent", "agent forwarding is off by default")
	assert.Contains(t, args, "--interactive")
	// The agent runs as root inside the container (podman --user 0), before it
	// drops to the session user itself.
	if i := indexOf(args, "--user"); assert.GreaterOrEqual(t, i, 0) {
		assert.Equal(t, "0", args[i+1], "podman exec must run the agent as root")
	}
	assert.Contains(t, args, AgentBinary)
	assert.Contains(t, args, "__serve")
	assert.Contains(t, args, "--host-key")
	assert.Contains(t, args, AgentHostKey)
	assert.Contains(t, args, "--env-file")
	assert.Contains(t, args, AgentEnvFile)

	// The session user is passed to __serve (the agent's own --user), after the
	// agent binary appears in the argv.
	serveIdx := indexOf(args, "__serve")
	require.GreaterOrEqual(t, serveIdx, 0)
	sessionUserFound := false
	for i := serveIdx; i+1 < len(args); i++ {
		if args[i] == "--user" && args[i+1] == "vscode" {
			sessionUserFound = true
		}
	}
	assert.True(t, sessionUserFound, "__serve must receive --user vscode")
	if i := indexOf(args, "--cwd"); assert.GreaterOrEqual(t, i, 0) {
		assert.Equal(t, "/workspace", args[i+1])
	}

	// With forwarding enabled, __serve gets --forward-agent.
	fwd := AgentServeArgs("ctr-1", "vscode", "/workspace", true)
	assert.Contains(t, fwd, "--forward-agent")
}

func TestInjectArchMismatch(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "exec" && args[2] == "uname" {
			return []byte("aarch64\n"), nil
		}
		return nil, nil
	}
	err := Inject(context.Background(), f, InjectOptions{Container: "c", HostArch: "amd64"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestInjectHappyPath(t *testing.T) {
	// Real host files for the binary + keys the copy step stats.
	dir := t.TempDir()
	agentBin := filepath.Join(dir, "devc")
	hostKey := filepath.Join(dir, "host_key")
	authKey := filepath.Join(dir, "auth.pub")
	for _, p := range []string{agentBin, hostKey, authKey} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}

	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "exec" && args[2] == "uname":
			return []byte("x86_64\n"), nil
		case len(args) >= 4 && args[0] == "exec" && args[3] == "version":
			return []byte("old-version\n"), nil // forces a fresh copy
		default:
			return nil, nil
		}
	}
	err := Inject(context.Background(), f, InjectOptions{
		Container:         "ctr",
		AgentSource:       agentBin,
		HostArch:          "amd64",
		Version:           "0.1.0",
		HostKeyFile:       hostKey,
		AuthorizedKeyFile: authKey,
		Env:               map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)

	calls := f.CallStrings()
	joined := strings.Join(calls, "\n")
	// Binary and both key files are copied to the agent dir.
	assert.Contains(t, joined, "cp "+agentBin+" ctr:"+AgentBinary)
	assert.Contains(t, joined, "cp "+hostKey+" ctr:"+AgentHostKey)
	assert.Contains(t, joined, "cp "+authKey+" ctr:"+AgentAuthKey)
	// Permissions are tightened, as root.
	assert.Contains(t, joined, "exec --user 0 ctr chmod 0755 "+AgentBinary)
	assert.Contains(t, joined, "exec --user 0 ctr chmod 0600 "+AgentHostKey)
	// Env file is written via a piped cat.
	assert.Contains(t, joined, "cat > "+AgentEnvFile)
}

func TestInjectSkipsCopyWhenUpToDate(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"devc", "hk", "ak"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600))
	}
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "exec" && args[2] == "uname":
			return []byte("x86_64\n"), nil
		case len(args) >= 4 && args[0] == "exec" && args[3] == "version":
			return []byte("0.1.0\n"), nil // matches => skip binary copy
		default:
			return nil, nil
		}
	}
	err := Inject(context.Background(), f, InjectOptions{
		Container: "ctr", AgentSource: filepath.Join(dir, "devc"), HostArch: "amd64", Version: "0.1.0",
		HostKeyFile: filepath.Join(dir, "hk"), AuthorizedKeyFile: filepath.Join(dir, "ak"),
	})
	require.NoError(t, err)
	joined := strings.Join(f.CallStrings(), "\n")
	assert.NotContains(t, joined, "cp "+filepath.Join(dir, "devc")+" ctr:"+AgentBinary,
		"binary copy should be skipped when version matches")
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
