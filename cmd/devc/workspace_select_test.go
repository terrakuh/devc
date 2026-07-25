package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/container"
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
}
