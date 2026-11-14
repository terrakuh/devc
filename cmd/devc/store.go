package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/terrakuh/devc/scaffold"
)

const storeUsage = `Usage:
  devc store [list]                          list the stores
  devc store add [--ref <ref>] <name> <git-url|folder>
                                             add a store of blocks and presets
  devc store update [<name>...]              fetch the latest commit (default: all)
  devc store remove <name>                   forget a store and drop its clone

A store has the layout of devc's own blocks: blocks/<name>/ and
presets/<name>.jsonc (see scaffold/README.md). Its blocks replace builtin
blocks of the same name; a later store wins over an earlier one.
`

// runStore implements `devc store`: the git repositories and folders devc init
// reads blocks and presets from besides its builtin ones.
func runStore(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	ctx := context.Background()
	stores, err := scaffold.LoadStores(storesFile())
	if err != nil {
		return err
	}
	cache := storeCacheDir()

	switch sub {
	case "list":
		if len(args) > 0 {
			return fmt.Errorf("store list takes no arguments")
		}
		printStores(os.Stdout, stores, cache)
		return nil

	case "add":
		fs := newFlagSet("store add")
		ref := fs.String("ref", "", "branch or tag to follow (default: the remote's default branch)")
		pos, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			return fmt.Errorf("usage: devc store add [--ref <ref>] <name> <git-url|folder>")
		}
		st := scaffold.Store{Name: pos[0], Ref: *ref}
		if scaffold.IsLocalPath(pos[1]) {
			if *ref != "" {
				return fmt.Errorf("--ref only applies to a git store")
			}
			if st.Path, err = filepath.Abs(expandHome(pos[1])); err != nil {
				return err
			}
			if fi, err := os.Stat(st.Path); err != nil || !fi.IsDir() {
				return fmt.Errorf("%s is not a folder", st.Path)
			}
		} else {
			st.URL = pos[1]
		}
		if err := stores.Add(st); err != nil {
			return err
		}
		if err := st.Fetch(ctx, cache); err != nil {
			return err
		}
		c, err := checkStore(stores, cache)
		if err != nil {
			if st.Path == "" {
				_ = os.RemoveAll(st.Dir(cache))
			}
			return err
		}
		if err := stores.Save(); err != nil {
			return err
		}
		blocks, presets := countFrom(c, st.Label(cache))
		fmt.Printf("added store %s: %d blocks, %d presets\n", st.Label(cache), blocks, presets)
		return nil

	case "update":
		names := args
		if len(names) == 0 {
			for _, st := range stores.Stores {
				names = append(names, st.Name)
			}
		}
		for _, n := range names {
			st := stores.Get(n)
			if st == nil {
				return fmt.Errorf("no store %q", n)
			}
			if st.Path != "" {
				fmt.Printf("%s: folder, read in place\n", n)
				continue
			}
			before := st.Commit(cache)
			if err := st.Fetch(ctx, cache); err != nil {
				return err
			}
			if after := st.Commit(cache); after == before {
				fmt.Printf("%s: up to date (%s)\n", n, after)
			} else {
				fmt.Printf("%s: %s -> %s\n", n, before, after)
			}
		}
		_, err := checkStore(stores, cache)
		return err

	case "remove":
		if len(args) != 1 {
			return fmt.Errorf("usage: devc store remove <name>")
		}
		st := stores.Get(args[0])
		if st == nil {
			return fmt.Errorf("no store %q", args[0])
		}
		dir := st.Dir(cache)
		local := st.Path != ""
		stores.Remove(args[0])
		if err := stores.Save(); err != nil {
			return err
		}
		// A folder store belongs to the user; only our own clone goes.
		if !local {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
		fmt.Printf("removed store %s\n", args[0])
		return nil

	case "help", "-h", "--help":
		fmt.Print(storeUsage)
		return nil
	}
	return fmt.Errorf("unknown store command %q\n\n%s", sub, storeUsage)
}

// loadCatalog is the builtin catalog with every store added, in order. A git
// store that has no clone yet (a copied config) is fetched first.
func loadCatalog(ctx context.Context) (*scaffold.Catalog, error) {
	stores, err := scaffold.LoadStores(storesFile())
	if err != nil {
		return nil, err
	}
	cache := storeCacheDir()
	for _, st := range stores.Stores {
		if !st.Fetched(cache) {
			fmt.Fprintf(os.Stderr, "fetching store %s\n", st.Name)
			if err := st.Fetch(ctx, cache); err != nil {
				return nil, fmt.Errorf("%w (devc store remove %s drops it)", err, st.Name)
			}
		}
	}
	return checkStore(stores, cache)
}

// checkStore builds the full catalog and checks that every block and preset
// finds the blocks it names.
func checkStore(stores *scaffold.Stores, cache string) (*scaffold.Catalog, error) {
	cat, err := scaffold.Builtin()
	if err != nil {
		return nil, err
	}
	for _, st := range stores.Stores {
		c, err := st.Load(cache)
		if err != nil {
			return nil, err
		}
		cat.Add(c)
	}
	return cat, cat.Check()
}

func countFrom(c *scaffold.Catalog, source string) (blocks, presets int) {
	for _, b := range c.Blocks {
		if b.Source == source {
			blocks++
		}
	}
	for _, p := range c.Presets {
		if p.Source == source {
			presets++
		}
	}
	return blocks, presets
}

func printStores(w io.Writer, stores *scaffold.Stores, cache string) {
	if len(stores.Stores) == 0 {
		fmt.Fprintln(w, "no stores; add one with: devc store add <name> <git-url|folder>")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tREF\tCOMMIT")
	for _, st := range stores.Stores {
		source, ref, commit := st.URL, st.Ref, st.Commit(cache)
		if st.Path != "" {
			source, ref, commit = st.Path, "-", "(folder)"
		}
		if ref == "" {
			ref = "default"
		}
		if commit == "" {
			commit = "(not fetched)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", st.Name, source, ref, commit)
	}
	tw.Flush()
}

// parseInterleaved parses flags that may come before, between or after the
// positional arguments, and returns the positional ones.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// storesFile is ~/.config/devc/stores.json (or under XDG_CONFIG_HOME).
func storesFile() string {
	return filepath.Join(filepath.Dir(devcSSHConfigDir()), "stores.json")
}

// storeCacheDir holds the clones of git stores: ~/.cache/devc/stores (or under
// XDG_CACHE_HOME). Everything in it can be fetched again.
func storeCacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "devc", "stores")
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
