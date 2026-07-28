package container

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/terrakuh/devc/runtime"
)

func TestListEnumeratesLabelledContainers(t *testing.T) {
	byID := map[string]Info{
		"id-one": {
			ID:     "id-one",
			State:  ContainerState{Status: "running", Running: true},
			Config: ContainerConfig{Labels: map[string]string{LabelID: "one-1111", LabelName: "one"}},
		},
		"id-two": {
			ID:     "id-two",
			State:  ContainerState{Status: "exited"},
			Config: ContainerConfig{Labels: map[string]string{LabelID: "two-2222", LabelName: "two"}},
		},
	}

	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte("id-one\nid-two\n"), nil
		}
		// inspect --format {{json .}} <ref>
		ref := args[len(args)-1]
		info, ok := byID[ref]
		if !ok {
			return nil, nil // triggers ErrNoSuchObject; List skips it
		}
		return json.Marshal(info)
	}

	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	names := []string{infos[0].Config.Labels[LabelName], infos[1].Config.Labels[LabelName]}
	assert.ElementsMatch(t, []string{"one", "two"}, names)

	// The enumeration filters on the devc id label.
	psCall := f.FindCall("ps")
	require.NotNil(t, psCall)
	assert.Contains(t, psCall, "label="+LabelID)
}

func TestListCollapsesComposeProject(t *testing.T) {
	// A single-container workspace plus a compose workspace whose project (devc-web-abcd1234)
	// has two service containers - only the running one should decide the row's state.
	byID := map[string]Info{
		"single": {
			ID:     "single",
			State:  ContainerState{Status: "running", Running: true},
			Config: ContainerConfig{Labels: map[string]string{LabelID: "solo-11111111", LabelName: "solo", LabelLocal: "/home/me/solo"}},
		},
		"web-app": {
			ID:     "web-app",
			State:  ContainerState{Status: "running", Running: true},
			Config: ContainerConfig{Labels: map[string]string{LabelComposeProject: "devc-web-abcd1234", LabelComposeService: "app"}},
		},
		"web-db": {
			ID:     "web-db",
			State:  ContainerState{Status: "exited"},
			Config: ContainerConfig{Labels: map[string]string{LabelComposeProject: "devc-web-abcd1234", LabelComposeService: "db"}},
		},
	}

	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" {
			if containsArg(args, "label="+LabelComposeProject) {
				return []byte("web-db\nweb-app\n"), nil // db first, app (running) second
			}
			return []byte("single\n"), nil // the LabelID query
		}
		ref := args[len(args)-1]
		info, ok := byID[ref]
		if !ok {
			return nil, nil
		}
		return json.Marshal(info)
	}

	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, infos, 2, "compose project collapses to one row alongside the single container")

	var compose *Info
	for _, info := range infos {
		if info.Config.Labels[LabelID] == "web-abcd1234" {
			compose = info
		}
	}
	require.NotNil(t, compose, "compose workspace present")
	assert.True(t, compose.Running(), "row is running because a service is up despite db being exited")
	assert.Empty(t, compose.Config.Labels[LabelName], "name is the caller's to backfill")
	assert.Empty(t, compose.Config.Labels[LabelLocal], "compose containers carry no folder label")
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestListSkipsVanishedContainers(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte("ghost\n"), nil
		}
		return nil, nil // inspect finds nothing -> ErrNoSuchObject
	}
	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	assert.Empty(t, infos)
}
