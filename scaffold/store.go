package scaffold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Store is a source of blocks and presets besides the builtin ones: a git
// repository (cloned into the cache) or a local folder (read in place, handy
// while writing blocks). Either way it has the same layout as this package:
// blocks/<name>/ and presets/<name>.jsonc.
type Store struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	// Ref is the branch or tag to follow; empty means the remote's default.
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"`
}

// Stores is the configured list of stores, in the order they are added to the
// catalog: a later store's blocks replace an earlier one's of the same name.
type Stores struct {
	// File is where the list is saved; not serialized.
	File   string  `json:"-"`
	Stores []Store `json:"stores"`
}

var storeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// LoadStores reads the store list from file; a missing file is an empty list.
func LoadStores(file string) (*Stores, error) {
	s := &Stores{File: file}
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return s, nil
}

// Save writes the store list back to its file.
func (s *Stores) Save() error {
	data, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.File), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.File, append(data, '\n'), 0o644)
}

// Get returns the store called name, or nil.
func (s *Stores) Get(name string) *Store {
	for i := range s.Stores {
		if s.Stores[i].Name == name {
			return &s.Stores[i]
		}
	}
	return nil
}

// Add appends st, refusing a duplicate or malformed name.
func (s *Stores) Add(st Store) error {
	if !storeName.MatchString(st.Name) || st.Name == BuiltinSource {
		return fmt.Errorf("invalid store name %q: use lowercase letters, digits, '.', '_' and '-', and not %q", st.Name, BuiltinSource)
	}
	if s.Get(st.Name) != nil {
		return fmt.Errorf("store %q already exists", st.Name)
	}
	s.Stores = append(s.Stores, st)
	return nil
}

// Remove drops the store called name and reports whether it existed.
func (s *Stores) Remove(name string) bool {
	for i := range s.Stores {
		if s.Stores[i].Name == name {
			s.Stores = append(s.Stores[:i], s.Stores[i+1:]...)
			return true
		}
	}
	return false
}

// IsLocalPath reports whether a store location names a folder rather than a
// git URL.
func IsLocalPath(location string) bool {
	return strings.HasPrefix(location, "/") || strings.HasPrefix(location, "~") || strings.HasPrefix(location, ".")
}

// Dir is the folder the store's blocks are read from.
func (st Store) Dir(cacheDir string) string {
	if st.Path != "" {
		return st.Path
	}
	return filepath.Join(cacheDir, st.Name)
}

// Fetched reports whether a git store has a clone in cacheDir; local stores
// always count as fetched.
func (st Store) Fetched(cacheDir string) bool {
	if st.Path != "" {
		return true
	}
	_, err := os.Stat(filepath.Join(st.Dir(cacheDir), ".git"))
	return err == nil
}

// Fetch clones a git store into cacheDir, or brings an existing clone to the
// latest commit of its ref. Local stores need nothing. git's progress and any
// credential prompt go to the terminal.
func (st Store) Fetch(ctx context.Context, cacheDir string) error {
	if st.Path != "" {
		return nil
	}
	dir := st.Dir(cacheDir)
	if !st.Fetched(cacheDir) {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return err
		}
		// Clone next to the target and rename, so a failed clone leaves nothing
		// that looks like a store.
		tmp, err := os.MkdirTemp(cacheDir, "."+st.Name+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		args := []string{"clone", "--quiet", "--depth", "1"}
		if st.Ref != "" {
			args = append(args, "--branch", st.Ref)
		}
		if err := git(ctx, "", append(args, "--", st.URL, tmp)...); err != nil {
			return fmt.Errorf("store %s: %w", st.Name, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return os.Rename(tmp, dir)
	}
	ref := st.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if err := git(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", ref); err != nil {
		return fmt.Errorf("store %s: %w", st.Name, err)
	}
	if err := git(ctx, dir, "reset", "--quiet", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("store %s: %w", st.Name, err)
	}
	return nil
}

// Commit is the short commit a git store's clone is at; "" for a local store
// or a missing clone.
func (st Store) Commit(cacheDir string) string {
	if st.Path != "" || !st.Fetched(cacheDir) {
		return ""
	}
	out, err := exec.Command("git", "-C", st.Dir(cacheDir), "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Label names the store in block listings and generated files: name@commit
// for a git store, the bare name for a local one.
func (st Store) Label(cacheDir string) string {
	if c := st.Commit(cacheDir); c != "" {
		return st.Name + "@" + c
	}
	return st.Name
}

// Load reads the store's blocks and presets.
func (st Store) Load(cacheDir string) (*Catalog, error) {
	dir := st.Dir(cacheDir)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("store %s: %w", st.Name, err)
	}
	c, err := Load(os.DirFS(dir), st.Label(cacheDir))
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", st.Name, err)
	}
	return c, nil
}

func git(ctx context.Context, dir string, args ...string) error {
	sub := args[0]
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", sub, err)
	}
	return nil
}
