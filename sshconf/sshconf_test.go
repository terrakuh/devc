package sshconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleParams() Params {
	return Params{
		Alias:          "devc.shop",
		User:           "root",
		DevcBin:        "/usr/local/bin/devc",
		ConfigPath:     "/home/me/shop/.devcontainer/devcontainer.json",
		RuntimePath:    "/usr/bin/podman",
		WorkspaceName:  "shop",
		IdentityFile:   "/keys/id_ed25519",
		KnownHostsFile: "/keys/known_hosts",
		ControlDir:     "/keys",
		Forwards: []Forward{
			{HostPort: 5173, ContainerPort: 5173},
			{HostPort: 8080, ContainerPort: 8080},
		},
	}
}

func TestRenderContainsEssentials(t *testing.T) {
	out := Render(sampleParams())
	assert.Contains(t, out, "Host devc.shop")
	// Plain paths (no spaces/metacharacters) are left unquoted so a naive
	// ProxyCommand arg splitter parses them correctly.
	assert.Contains(t, out, "ProxyCommand          /usr/local/bin/devc ssh --stdio --start "+
		"--config /home/me/shop/.devcontainer/devcontainer.json --runtime /usr/bin/podman shop")
	assert.Contains(t, out, "IdentityFile          /keys/id_ed25519")
	assert.Contains(t, out, "StrictHostKeyChecking yes")
	assert.Contains(t, out, "UserKnownHostsFile    /keys/known_hosts")
	assert.Contains(t, out, "ControlPath           /keys/control-%r")
	assert.Contains(t, out, "LocalForward          8080 localhost:8080")
	assert.Contains(t, out, "LocalForward          5173 localhost:5173")
}

func TestRenderDeterministicForwardOrder(t *testing.T) {
	p := sampleParams()
	// Reversed input must still render sorted by host port.
	p.Forwards = []Forward{{HostPort: 8080, ContainerPort: 8080}, {HostPort: 5173, ContainerPort: 5173}}
	out := Render(p)
	assert.Less(t, strings.Index(out, "5173 localhost"), strings.Index(out, "8080 localhost"))
}

func TestRenderDefaultUser(t *testing.T) {
	p := sampleParams()
	p.User = ""
	assert.Contains(t, Render(p), "User                  root")
}

func TestRenderQuotesPathsWithSpaces(t *testing.T) {
	p := sampleParams()
	p.DevcBin = "/opt/my tools/devc"
	p.ConfigPath = "/home/me/a b/.devcontainer/devcontainer.json"
	out := Render(p)
	// A path containing a space must be single-quoted so the shell (or the
	// editor's ProxyCommand splitter) keeps it as one argument.
	assert.Contains(t, out, "ProxyCommand          '/opt/my tools/devc' ssh --stdio --start "+
		"--config '/home/me/a b/.devcontainer/devcontainer.json' --runtime /usr/bin/podman shop")
}

func TestRenderForwardAgentDefaultOff(t *testing.T) {
	out := Render(sampleParams())
	assert.Contains(t, out, "ForwardAgent          no")
	assert.NotContains(t, out, "--forward-agent")
}

func TestRenderForwardAgentOn(t *testing.T) {
	p := sampleParams()
	p.ForwardAgent = true
	out := Render(p)
	assert.Contains(t, out, "ForwardAgent          yes")
	// The ProxyCommand also carries it so the agent side permits forwarding.
	assert.Contains(t, out, "--forward-agent shop")
}

func TestRenderOmitsEmptyRuntime(t *testing.T) {
	p := sampleParams()
	p.RuntimePath = ""
	out := Render(p)
	assert.NotContains(t, out, "--runtime")
	assert.Contains(t, out, "--config /home/me/shop/.devcontainer/devcontainer.json shop")
}

func TestEnsureIncludeIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".ssh", "config")
	include := filepath.Join(dir, ".config", "devc", "ssh.config")

	changed, err := EnsureInclude(cfg, include)
	require.NoError(t, err)
	assert.True(t, changed, "first insertion should change the file")

	// Second call is a no-op.
	changed, err = EnsureInclude(cfg, include)
	require.NoError(t, err)
	assert.False(t, changed, "include already present")

	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "Include "+include), "include must appear exactly once")
}

func TestEnsureIncludeBacksUpAndPreserves(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	cfg := filepath.Join(sshDir, "config")
	original := "Host example\n    HostName example.com\n"
	require.NoError(t, os.WriteFile(cfg, []byte(original), 0o600))

	include := filepath.Join(dir, "devc.ssh.config")
	changed, err := EnsureInclude(cfg, include)
	require.NoError(t, err)
	assert.True(t, changed)

	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	// Include is on top; original content preserved below.
	assert.True(t, strings.HasPrefix(string(data), "# Added by devc"))
	assert.Contains(t, string(data), original)
	assert.Less(t, strings.Index(string(data), "Include "), strings.Index(string(data), "Host example"))

	// A timestamped backup of the original exists.
	entries, _ := os.ReadDir(sshDir)
	var backups int
	for _, e := range entries {
		if strings.Contains(e.Name(), "devc-backup") {
			backups++
		}
	}
	assert.Equal(t, 1, backups, "one backup written")
}
