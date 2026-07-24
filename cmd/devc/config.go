package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/terrakuh/devc/config"
)

// runConfig implements `devc config`: it resolves the devcontainer.json for the
// given path and prints the Spec as JSON. With --raw it prints the decoded
// (post-substitution) Raw document instead, which is useful for debugging
// parsing and variable expansion.
func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	path := fs.String("path", ".", "project directory or path to a devcontainer.json")
	configFile := fs.String("config", "", "explicit devcontainer.json to use (overrides --path discovery)")
	raw := fs.Bool("raw", false, "print the decoded Raw config after substitution instead of the resolved Spec")
	quiet := fs.Bool("q", false, "suppress warnings on stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *raw {
		return runConfigRaw(*path, *configFile, *quiet)
	}

	spec, warnings, err := config.LoadSpec(*path, *configFile)
	if err != nil {
		return err
	}
	if !*quiet {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	return printJSON(spec)
}

func runConfigRaw(path, configFile string, quiet bool) error {
	l, err := config.Load(path, configFile)
	if err != nil {
		return err
	}
	name := l.Raw.Name
	if name == "" {
		name = defaultName(l.LocalWorkspaceFolder)
	}
	id := config.WorkspaceID(name, l.LocalWorkspaceFolder)
	if err := config.Substitute(l, id); err != nil {
		return err
	}
	if !quiet {
		for _, w := range l.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	return printJSON(l.Raw)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func defaultName(localFolder string) string {
	// Mirror config.Resolve's fallback without exporting internals.
	base := localFolder
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			return base[i+1:]
		}
	}
	return base
}
