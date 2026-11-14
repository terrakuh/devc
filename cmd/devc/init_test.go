package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/scaffold"
)

func builtinCatalog(t *testing.T) *scaffold.Catalog {
	t.Helper()
	cat, err := scaffold.Builtin()
	require.NoError(t, err)
	return cat
}

func fileByName(files []scaffold.File, name string) string {
	for _, f := range files {
		if f.Name == name {
			return string(f.Data)
		}
	}
	return ""
}

func TestWizard(t *testing.T) {
	answers := []string{
		"go",          // preset
		"nope",        // unknown block, list again
		"private-git", // toggle on
		"",            // done with blocks
		"",            // fedora.version: default
		"v0.20.0",     // go.gopls
		"",            // claude.channel: default
		"",            // private-git.host: required, asked again
		"git.example.com",
		"2222", // private-git.sshPort
		"",     // private-git.hostKey: default (empty)
		"Demo", // name
		"p",    // print, ask again
		"",     // write
	}
	var out strings.Builder
	w := &wizard{
		cat: builtinCatalog(t),
		in:  bufio.NewScanner(strings.NewReader(strings.Join(answers, "\n") + "\n")),
		out: &out,
	}
	files, command, err := w.run(initRequest{name: "proj"})
	require.NoError(t, err, out.String())

	require.Equal(t, "devc init --with go,claude,private-git --set go.gopls=v0.20.0 --set private-git.host=git.example.com --set private-git.sshPort=2222 --name Demo", command)
	require.Contains(t, out.String(), `no block "nope"`)
	require.Contains(t, out.String(), "  required\n")
	require.Contains(t, out.String(), "==> .devcontainer/post-create.sh <==")
	require.Contains(t, fileByName(files, "devcontainer.json"), `"name": "Demo"`)
	require.Contains(t, fileByName(files, "Containerfile"), "gopls@v0.20.0")
	require.Contains(t, fileByName(files, "post-create.sh"), "GIT_SSH_PORT=2222")
}

func TestWizardAbort(t *testing.T) {
	w := &wizard{cat: builtinCatalog(t), in: bufio.NewScanner(strings.NewReader("go\n")), out: &strings.Builder{}}
	_, _, err := w.run(initRequest{name: "proj"})
	require.ErrorIs(t, err, errAborted)
}

func TestSelectBlocks(t *testing.T) {
	cat := builtinCatalog(t)

	names, params, err := selectBlocks(cat, initRequest{
		preset:  "go-web",
		with:    []string{"fuse", "go"},
		without: []string{"playwright"},
		params:  map[string]map[string]string{"go": {"gopls": "v1"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"go", "node", "postgres-client", "claude", "fuse"}, names)
	require.Equal(t, "v1", params["go"]["gopls"])

	_, _, err = selectBlocks(cat, initRequest{preset: "go", without: []string{"fedora"}})
	require.EqualError(t, err, "cannot drop fedora: go requires it")

	_, _, err = selectBlocks(cat, initRequest{preset: "nope"})
	require.ErrorContains(t, err, `unknown preset "nope"`)
}

func TestRenderRequestNeedsSelection(t *testing.T) {
	_, err := renderRequest(builtinCatalog(t), initRequest{name: "x"})
	require.ErrorContains(t, err, "nothing selected")
}

func TestParamFlag(t *testing.T) {
	p := paramFlag{}
	require.NoError(t, p.Set("private-git.hostKey=ssh-ed25519 AAA=="))
	require.Equal(t, "ssh-ed25519 AAA==", p["private-git"]["hostKey"])
	require.Error(t, p.Set("nodot=x"))
	require.Error(t, p.Set("a.b"))
}

func TestWriteFilesRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	files, err := renderRequest(builtinCatalog(t), initRequest{preset: "go-web", name: "x"})
	require.NoError(t, err)
	require.NoError(t, checkExisting(dir))
	require.NoError(t, writeFiles(dir, files))

	fi, err := os.Stat(filepath.Join(dir, ".devcontainer", "post-create.sh"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	require.ErrorContains(t, checkExisting(dir), ".devcontainer/devcontainer.json, .devcontainer/Containerfile, .devcontainer/post-create.sh already exist")
}

func TestShellQuote(t *testing.T) {
	require.Equal(t, "go.gopls=v1", shellQuote("go.gopls=v1"))
	require.Equal(t, `'a.b=it'\''s here'`, shellQuote("a.b=it's here"))
}
