package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		return fakeInspect(t, args, byID)
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
		return fakeInspect(t, args, byID)
	}

	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	// Every container is inspected in one runtime call, however many there are.
	var inspects []string
	for _, c := range f.CallStrings() {
		if strings.HasPrefix(c, "inspect ") {
			inspects = append(inspects, c)
		}
	}
	assert.Equal(t, []string{"inspect --format {{json .}} single web-db web-app"}, inspects)
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

// fakeInspect answers a batched `inspect --format {{json .}} <ref>...` the way
// podman and docker do: one JSON line per known ref, and a "no such object"
// failure if any ref is unknown.
func fakeInspect(t *testing.T, args []string, byID map[string]Info) ([]byte, error) {
	t.Helper()
	require.Equal(t, []string{"inspect", "--format", "{{json .}}"}, args[:3])
	var out []byte
	var err error
	for _, ref := range args[3:] {
		info, ok := byID[ref]
		if !ok {
			err = fmt.Errorf("Error: no such object: %q", ref)
			continue
		}
		b, mErr := json.Marshal(info)
		require.NoError(t, mErr)
		out = append(append(out, b...), '\n')
	}
	return out, err
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
			if containsArg(args, "label="+LabelID) {
				return []byte("ghost\nalive\n"), nil
			}
			return nil, nil
		}
		return fakeInspect(t, args, map[string]Info{
			"alive": {ID: "alive", Config: ContainerConfig{Labels: map[string]string{LabelID: "alive-1111"}}},
		})
	}
	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "alive", infos[0].ID)
}

func TestListPropagatesInspectFailure(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("one\n"), nil
		}
		return nil, errors.New("cannot connect to the daemon")
	}
	_, err := List(context.Background(), f)
	assert.ErrorContains(t, err, "cannot connect")
}

func TestListWithoutContainersSkipsInspect(t *testing.T) {
	f := runtime.NewFake()
	infos, err := List(context.Background(), f)
	require.NoError(t, err)
	assert.Empty(t, infos)
	assert.Nil(t, f.FindCall("inspect"))
}
