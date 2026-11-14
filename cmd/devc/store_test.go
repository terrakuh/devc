package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	write := func(name, data string) {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(data), 0o644))
	}
	// Replaces the builtin go block, adds a preset that uses it.
	write("blocks/go/block.jsonc", `{"description": "my go", "requires": ["fedora"]}`)
	write("presets/mine.jsonc", `{"description": "mine", "blocks": ["go", "claude"]}`)

	require.NoError(t, runStore([]string{"add", "local", dir}))
	require.ErrorContains(t, runStore([]string{"add", "local", dir}), "already exists")

	cat, err := loadCatalog(context.Background())
	require.NoError(t, err)
	require.Equal(t, "my go", cat.Blocks["go"].Description)
	require.Equal(t, "local", cat.Blocks["go"].Source)
	require.Equal(t, "builtin", cat.Blocks["claude"].Source)
	require.Equal(t, []string{"go", "claude"}, cat.Presets["mine"].Blocks)

	require.NoError(t, runStore([]string{"remove", "local"}))
	_, err = os.Stat(dir)
	require.NoError(t, err, "removing a folder store must not delete the folder")
	cat, err = loadCatalog(context.Background())
	require.NoError(t, err)
	require.Equal(t, "builtin", cat.Blocks["go"].Source)
}

func TestStoreAddRejectsBrokenStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	p := filepath.Join(dir, "blocks", "x", "block.jsonc")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(`{"requires": ["nope"]}`), 0o644))

	require.ErrorContains(t, runStore([]string{"add", "broken", dir}), `requires unknown block "nope"`)
	_, err := os.Stat(storesFile())
	require.ErrorIs(t, err, os.ErrNotExist, "a broken store must not be saved")
}

func TestParseInterleaved(t *testing.T) {
	for _, args := range [][]string{
		{"--ref", "v1", "name", "url"},
		{"name", "--ref", "v1", "url"},
		{"name", "url", "--ref=v1"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		ref := fs.String("ref", "", "")
		pos, err := parseInterleaved(fs, args)
		require.NoError(t, err)
		require.Equal(t, []string{"name", "url"}, pos)
		require.Equal(t, "v1", *ref)
	}
}
