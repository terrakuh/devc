package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/terrakuh/devc/config"
)

func TestRenamedFrom(t *testing.T) {
	spec := &config.Spec{
		ID:                   "new-1a2b3c4d",
		Name:                 "new",
		LocalWorkspaceFolder: "/home/me/proj",
		ConfigPath:           "/home/me/proj/.devcontainer/devcontainer.json",
	}
	refs := []workspaceRef{
		{ID: "new-1a2b3c4d", Name: "new", Local: "/home/me/proj", ConfigPath: spec.ConfigPath},
		{ID: "old-1a2b3c4d", Name: "old", Local: "/home/me/proj", ConfigPath: spec.ConfigPath},
		{ID: "legacy-1a2b3c4d", Name: "legacy", Local: "/home/me/proj"},
		{ID: "other-1a2b3c4d", Name: "other", Local: "/home/me/proj", ConfigPath: "/home/me/proj/.devcontainer/other/devcontainer.json"},
		{ID: "elsewhere-99887766", Name: "elsewhere", Local: "/home/me/elsewhere", ConfigPath: "/home/me/elsewhere/.devcontainer.json"},
		{ID: "unknown-abcdef01", Name: "unknown"},
	}

	got := renamedFrom(spec, refs)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []string{"legacy-1a2b3c4d", "old-1a2b3c4d"}, ids,
		"same folder and config (or unrecorded config) is the renamed workspace; the current one, sibling configs, other folders and unknown folders are not")
}
