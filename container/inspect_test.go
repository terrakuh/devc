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
