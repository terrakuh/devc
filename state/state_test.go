package state

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir, err := For("demo-1a2b3c4d")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir.Root) != "demo-1a2b3c4d" {
		t.Fatalf("unexpected root %s", dir.Root)
	}

	// Fresh load returns a zero state, not an error.
	s, err := dir.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.ContainerID != "" {
		t.Fatalf("expected empty state, got %+v", s)
	}

	s.ID = "demo-1a2b3c4d"
	s.ContainerID = "abc123"
	s.ConfigHash = "hash"
	s.Hooks = map[string]string{"onCreate": "2026-07-23T10:00:00Z"}
	if err := dir.Save(s); err != nil {
		t.Fatal(err)
	}

	got, err := dir.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "abc123" || got.Hooks["onCreate"] == "" {
		t.Fatalf("round trip lost data: %+v", got)
	}

	if err := dir.Remove(); err != nil {
		t.Fatal(err)
	}
	// After removal, For recreates and Load is zero again.
	dir2, _ := For("demo-1a2b3c4d")
	s2, _ := dir2.Load()
	if s2.ContainerID != "" {
		t.Fatalf("state survived removal: %+v", s2)
	}
}

func TestLogsDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, _ := For("x")
	logs, err := dir.LogsDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(logs) != "logs" {
		t.Fatalf("bad logs dir %s", logs)
	}
}
