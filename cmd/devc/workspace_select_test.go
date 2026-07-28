package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/state"
)

func info(id, name, local string) *container.Info {
	return &container.Info{Config: container.ContainerConfig{Labels: map[string]string{
		container.LabelID:    id,
		container.LabelName:  name,
		container.LabelLocal: local,
	}}}
}

func TestSelectWorkspaceFolder(t *testing.T) {
	infos := []*container.Info{
		info("shop-1a2b3c4d", "shop", "/home/me/shop"),
		info("api-5e6f7a8b", "api", "/home/me/api"),
	}

	t.Run("by name", func(t *testing.T) {
		f, err := selectWorkspaceFolder(infos, "shop")
		require.NoError(t, err)
		assert.Equal(t, "/home/me/shop", f)
	})

	t.Run("by id", func(t *testing.T) {
		f, err := selectWorkspaceFolder(infos, "api-5e6f7a8b")
		require.NoError(t, err)
		assert.Equal(t, "/home/me/api", f)
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := selectWorkspaceFolder(infos, "nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no workspace named")
	})

	t.Run("ambiguous", func(t *testing.T) {
		dup := []*container.Info{
			info("shop-1a2b3c4d", "shop", "/home/me/shop"),
			info("shop-99887766", "shop", "/home/me/other/shop"),
		}
		_, err := selectWorkspaceFolder(dup, "shop")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
	})

	t.Run("matched but folder unknown", func(t *testing.T) {
		_, err := selectWorkspaceFolder([]*container.Info{info("web-abcd1234", "web", "")}, "web")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no recorded folder", "must not claim the workspace does not exist")
	})
}

// TestBackfillFromState covers the compose path: container.List can only recover
// the id, and the name/folder come back from the workspace state dir.
func TestBackfillFromState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := state.For("web-abcd1234")
	require.NoError(t, err)
	require.NoError(t, dir.Save(&state.State{
		ID:          "web-abcd1234",
		Name:        "Web App",
		LocalFolder: "/home/me/web",
	}))

	t.Run("compose row gains name and folder", func(t *testing.T) {
		row := info("web-abcd1234", "", "")
		backfillFromState(row)
		assert.Equal(t, "Web App", row.Config.Labels[container.LabelName])
		assert.Equal(t, "/home/me/web", row.Config.Labels[container.LabelLocal])
	})

	t.Run("labels win over state", func(t *testing.T) {
		row := info("web-abcd1234", "labelled", "/from/label")
		backfillFromState(row)
		assert.Equal(t, "labelled", row.Config.Labels[container.LabelName])
		assert.Equal(t, "/from/label", row.Config.Labels[container.LabelLocal])
	})

	t.Run("no state falls back to the id slug", func(t *testing.T) {
		row := info("other-99887766", "", "")
		backfillFromState(row)
		assert.Equal(t, "other", row.Config.Labels[container.LabelName], "name derived from the id")
		assert.Empty(t, row.Config.Labels[container.LabelLocal], "folder stays unknown")
	})
}
