// Package state manages devc's per-workspace host-side directories and the
// state.json that records facts we must not keep inside the container: which
// container identity a workspace last used, which lifecycle hooks have run, the
// cached environment probe, and the injected agent version.
//
// The layout under the user's data dir (XDG_DATA_HOME or ~/.local/share):
//
//	devc/<id>/
//	  state.json
//	  logs/
//	  (ssh keys live here too)
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State is the persisted per-workspace record: the workspace's identity and
// origin, the container identity, which hooks have run, the cached env probe,
// and the injected agent version.
//
// Name and LocalFolder duplicate what a single container carries as devc labels.
// They are recorded here because compose creates the service containers itself
// and devc cannot label them, so for a compose workspace this is the only place
// the display name and originating folder survive - `devc list` and -n/--name
// read them back from here.
type State struct {
	ID             string            `json:"id"`
	Name           string            `json:"name,omitempty"`
	LocalFolder    string            `json:"localWorkspaceFolder,omitempty"`
	ContainerID    string            `json:"containerID,omitempty"`
	ComposeProject string            `json:"composeProject,omitempty"`
	ConfigHash     string            `json:"configHash,omitempty"`
	Hooks          map[string]string `json:"hooks,omitempty"` // hook name -> RFC3339 timestamp
	EnvProbe       map[string]string `json:"envProbe,omitempty"`
	AgentVersion   string            `json:"agentVersion,omitempty"`
}

// Peek reads a workspace's state without creating its directory, for read-only
// callers (list, -n/--name) that must not conjure state dirs for ids they are
// merely inspecting. A workspace with no state yet yields a zero State.
func Peek(id string) (*State, error) {
	base, err := dataHome()
	if err != nil {
		return nil, err
	}
	d := &Dir{Root: filepath.Join(base, "devc", id)}
	return d.Load()
}

// Dir is a workspace's state directory.
type Dir struct {
	Root string // .../devc/<id>
}

// For returns the state directory for a workspace id, creating it (0700) if
// needed.
func For(id string) (*Dir, error) {
	base, err := dataHome()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "devc", id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Dir{Root: root}, nil
}

// LogsDir returns (and creates) the per-workspace logs directory.
func (d *Dir) LogsDir() (string, error) {
	p := filepath.Join(d.Root, "logs")
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// Path returns a path inside the workspace directory.
func (d *Dir) Path(name string) string { return filepath.Join(d.Root, name) }

func (d *Dir) statePath() string { return filepath.Join(d.Root, "state.json") }

// Load reads state.json, returning a zero State (not an error) when it does not
// yet exist.
func (d *Dir) Load() (*State, error) {
	b, err := os.ReadFile(d.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", d.statePath(), err)
	}
	return &s, nil
}

// Save writes state.json atomically (write temp + rename).
func (d *Dir) Save(s *State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.statePath())
}

// Remove deletes the entire workspace state directory.
func (d *Dir) Remove() error { return os.RemoveAll(d.Root) }

// dataHome resolves the base data directory (XDG_DATA_HOME or ~/.local/share).
func dataHome() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
