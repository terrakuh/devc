package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, repo, msg string) string {
	t.Helper()
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
	return run(t, repo, "git", "rev-parse", "--short", "HEAD")
}

func TestGitStore(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "blocks/x/block.jsonc"), `{"description": "one", "requires": ["fedora"]}`)
	first := commitAll(t, repo, "one")

	cache := t.TempDir()
	st := Store{Name: "mine", URL: "file://" + repo}
	if st.Fetched(cache) {
		t.Fatal("fetched before Fetch")
	}
	if err := st.Fetch(context.Background(), cache); err != nil {
		t.Fatal(err)
	}
	if got := st.Commit(cache); got != first {
		t.Fatalf("commit = %q, want %q", got, first)
	}
	c, err := st.Load(cache)
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Blocks["x"]; b == nil || b.Source != "mine@"+first {
		t.Fatalf("block x = %+v", b)
	}

	writeFile(t, filepath.Join(repo, "blocks/x/block.jsonc"), `{"description": "two", "requires": ["fedora"]}`)
	second := commitAll(t, repo, "two")
	if err := st.Fetch(context.Background(), cache); err != nil {
		t.Fatal(err)
	}
	if got := st.Commit(cache); got != second {
		t.Fatalf("after update commit = %q, want %q", got, second)
	}
	c, err = st.Load(cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Blocks["x"].Description; got != "two" {
		t.Fatalf("description = %q after update", got)
	}

	// A store block requiring a builtin one resolves once both are together,
	// and its origin ends up in the generated files.
	all, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	all.Add(c)
	if err := all.Check(); err != nil {
		t.Fatal(err)
	}
	sel, err := all.Resolve([]string{"x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := sel.Render("t")
	if err != nil {
		t.Fatal(err)
	}
	if want := "(blocks: fedora, x from mine@" + second + ")"; !strings.Contains(string(files[0].Data), want) {
		t.Fatalf("devcontainer.json lacks %q:\n%s", want, files[0].Data)
	}
}

func TestGitStoreFetchFailureLeavesNothing(t *testing.T) {
	cache := t.TempDir()
	st := Store{Name: "bad", URL: "file://" + filepath.Join(t.TempDir(), "missing")}
	if err := st.Fetch(context.Background(), cache); err == nil {
		t.Fatal("fetch of a missing repo succeeded")
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 0 {
		t.Fatalf("cache not empty after a failed clone: %v", entries)
	}
}

func TestStoresFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "devc", "stores.json")
	s, err := LoadStores(file)
	if err != nil || len(s.Stores) != 0 {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	if err := s.Add(Store{Name: "a", URL: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Store{Name: "a", URL: "https://y"}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	for _, bad := range []string{"", "Upper", "a/b", "builtin", "-x"} {
		if err := s.Add(Store{Name: bad}); err == nil {
			t.Fatalf("name %q accepted", bad)
		}
	}
	if err := s.Add(Store{Name: "b", Path: "/blocks"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s, err = LoadStores(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Stores) != 2 || s.Stores[0].URL != "https://x" || s.Stores[1].Path != "/blocks" {
		t.Fatalf("round trip: %+v", s.Stores)
	}
	if !s.Remove("a") || s.Remove("a") || len(s.Stores) != 1 {
		t.Fatalf("remove: %+v", s.Stores)
	}
}
