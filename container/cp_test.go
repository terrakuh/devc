package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// fakeContainer backs a FakeRunner with a host directory standing in for the
// container's AgentDir: Inject's in-container scripts run under the host's sh
// (with a stub uname reporting arch) and `cp` copies into the directory, so the
// scripts themselves are exercised, not just the argv.
type fakeContainer struct {
	f    *runtime.FakeRunner
	root string // stands in for AgentDir
}

func newFakeContainer(t *testing.T, arch string) *fakeContainer {
	t.Helper()
	c := &fakeContainer{f: runtime.NewFake(), root: filepath.Join(t.TempDir(), "devc")}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\necho "+arch+"\n"), 0o755))

	runScript := func(args []string, stdin io.Reader) ([]byte, error) {
		i := slices.Index(args, "-c")
		require.Positive(t, i)
		prefix := slices.DeleteFunc(slices.Clone(args[1:i]), func(a string) bool { return a == "--interactive" })
		require.Equal(t, []string{"--user", "0", "ctr", "sh"}, prefix, "runs sh as root")
		rest := slices.Clone(args[i+2:])
		for j, a := range rest {
			if a == AgentDir {
				rest[j] = c.root
			}
		}
		cmd := exec.Command("sh", append([]string{"-c", args[i+1]}, rest...)...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		cmd.Stdin = stdin
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, stderr.String())
		}
		return out, nil
	}
	c.f.OutputFunc = func(args []string) ([]byte, error) {
		switch args[0] {
		case "exec":
			return runScript(args, nil)
		case "cp":
			dest, ok := strings.CutPrefix(args[2], "ctr:"+AgentDir+"/")
			require.True(t, ok, "cp target %q", args[2])
			b, err := os.ReadFile(args[1])
			require.NoError(t, err)
			st, err := os.Stat(args[1])
			require.NoError(t, err)
			return nil, os.WriteFile(filepath.Join(c.root, dest), b, st.Mode().Perm())
		}
		t.Fatalf("unexpected runtime call %v", args)
		return nil, nil
	}
	c.f.RunFunc = func(args []string, rio runtime.IO) error {
		_, err := runScript(args, rio.Stdin)
		return err
	}
	return c
}

// verbs lists the runtime subcommand of every call made, in order.
func (c *fakeContainer) verbs() []string {
	var out []string
	for _, call := range c.f.Calls {
		out = append(out, call[0])
	}
	return out
}

func (c *fakeContainer) file(t *testing.T, name string) (string, os.FileMode) {
	t.Helper()
	p := filepath.Join(c.root, name)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	st, err := os.Stat(p)
	require.NoError(t, err)
	return string(b), st.Mode().Perm()
}

// injectFixture writes the host files Inject reads: an agent binary (a script
// answering `version`) and the two keys.
func injectFixture(t *testing.T, agentVersion string) InjectOptions {
	t.Helper()
	dir := t.TempDir()
	opts := InjectOptions{
		Container:         "ctr",
		AgentSource:       filepath.Join(dir, "devc"),
		HostArch:          "amd64",
		Version:           agentVersion,
		HostKeyFile:       filepath.Join(dir, "host_key"),
		AuthorizedKeyFile: filepath.Join(dir, "auth.pub"),
		Env:               map[string]string{"SECRET": "s3cret", "A": "multi\nline"},
	}
	require.NoError(t, os.WriteFile(opts.AgentSource, []byte("#!/bin/sh\necho "+agentVersion+"\n"), 0o755))
	require.NoError(t, os.WriteFile(opts.HostKeyFile, []byte("-----BEGIN KEY-----\nhost\n-----END KEY-----\n"), 0o600))
	require.NoError(t, os.WriteFile(opts.AuthorizedKeyFile, []byte("ssh-ed25519 AAAA client\n"), 0o644))
	return opts
}

func TestInjectArchMismatch(t *testing.T) {
	c := newFakeContainer(t, "aarch64")
	err := Inject(context.Background(), c.f, injectFixture(t, "0.1.0"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
	assert.Equal(t, []string{"exec"}, c.verbs(), "nothing is copied into a mismatched container")
}

func TestInjectFreshContainer(t *testing.T) {
	c := newFakeContainer(t, "x86_64")
	opts := injectFixture(t, "0.1.0")
	require.NoError(t, Inject(context.Background(), c.f, opts))

	// Prepare, copy the binary, write the secrets: three runtime calls in all.
	assert.Equal(t, []string{"exec", "cp", "exec"}, c.verbs())
	assert.Equal(t, "cp "+opts.AgentSource+" ctr:"+AgentBinary, c.f.CallStrings()[1])

	st, err := os.Stat(c.root)
	require.NoError(t, err)
	assert.Equal(t, AgentDirMode, fmt.Sprintf("%04o", st.Mode().Perm()))

	_, mode := c.file(t, filepath.Base(AgentBinary))
	assert.Equal(t, os.FileMode(0o755), mode)
	hostKey, mode := c.file(t, filepath.Base(AgentHostKey))
	assert.Equal(t, "-----BEGIN KEY-----\nhost\n-----END KEY-----\n", hostKey)
	assert.Equal(t, os.FileMode(0o600), mode)
	authKey, mode := c.file(t, filepath.Base(AgentAuthKey))
	assert.Equal(t, "ssh-ed25519 AAAA client\n", authKey)
	assert.Equal(t, os.FileMode(0o600), mode)
	env, mode := c.file(t, filepath.Base(AgentEnvFile))
	assert.Equal(t, "A=multi line\nSECRET=s3cret\n", env)
	assert.Equal(t, os.FileMode(0o600), mode)
}

// TestInjectSecretsStayOffArgv: remoteEnv can carry tokens, and argv is
// visible to every process on the host and in the container.
func TestInjectSecretsStayOffArgv(t *testing.T) {
	c := newFakeContainer(t, "x86_64")
	require.NoError(t, Inject(context.Background(), c.f, injectFixture(t, "0.1.0")))
	joined := strings.Join(c.f.CallStrings(), "\n")
	assert.NotContains(t, joined, "s3cret")
	assert.NotContains(t, joined, "BEGIN KEY")
}

// TestInjectAgentDirIsTraversable pins the mode the SFTP subsystem needs to
// re-exec the agent as a non-root session user. 0700 breaks that while
// interactive ssh keeps working. See agent.TestSFTPReExecAsSessionUser.
func TestInjectAgentDirIsTraversable(t *testing.T) {
	mode, err := strconv.ParseUint(AgentDirMode, 8, 32)
	require.NoError(t, err)
	assert.NotZero(t, mode&0o001, "AgentDir must stay world-traversable (--x)")
}

func TestInjectSkipsCopyWhenUpToDate(t *testing.T) {
	c := newFakeContainer(t, "x86_64")
	opts := injectFixture(t, "0.1.0")
	require.NoError(t, Inject(context.Background(), c.f, opts))
	c.f.Calls = nil

	require.NoError(t, Inject(context.Background(), c.f, opts))
	assert.Equal(t, []string{"exec", "exec"}, c.verbs(), "binary copy is skipped when the version matches")
}

func TestInjectReplacesOutdatedAgentAndLooseSecrets(t *testing.T) {
	c := newFakeContainer(t, "x86_64")
	require.NoError(t, Inject(context.Background(), c.f, injectFixture(t, "0.0.9")))
	// An older devc left a world-readable env file behind.
	require.NoError(t, os.Chmod(filepath.Join(c.root, "env"), 0o644))
	c.f.Calls = nil

	require.NoError(t, Inject(context.Background(), c.f, injectFixture(t, "0.1.0")))
	assert.Equal(t, []string{"exec", "cp", "exec"}, c.verbs())
	_, mode := c.file(t, "env")
	assert.Equal(t, os.FileMode(0o600), mode)
}

func TestInjectReportsSecretsFailure(t *testing.T) {
	c := newFakeContainer(t, "x86_64")
	c.f.RunFunc = func([]string, runtime.IO) error {
		return errors.New("exit status 1")
	}
	err := Inject(context.Background(), c.f, injectFixture(t, "0.1.0"))
	assert.ErrorContains(t, err, "write agent credentials")
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
