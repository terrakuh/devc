package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// workspaceRow is one line of `devc list` (also the --json element).
type workspaceRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Local   string `json:"localWorkspaceFolder"`
	Runtime string `json:"runtime"`
}

// runList implements `devc list`: every workspace devc created, discovered by
// label - no local index required, so it sees workspaces from any directory.
func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	runtimeFlag := fs.String("runtime", "", "container runtime: podman or docker (default: autodetect)")
	jsonOut := fs.Bool("json", false, "emit the list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()

	r, err := runtime.Detect(*runtimeFlag)
	if err != nil {
		return err
	}
	infos, err := container.List(ctx, r)
	if err != nil {
		return err
	}

	rows := make([]workspaceRow, 0, len(infos))
	for _, info := range infos {
		labels := info.Config.Labels
		rows = append(rows, workspaceRow{
			ID:      labels[container.LabelID],
			Name:    labels[container.LabelName],
			State:   containerState(info),
			Local:   labels[container.LabelLocal],
			Runtime: r.Name(),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printWorkspaceRows(rows)
	return nil
}

// containerState is the display state for a container Info.
func containerState(info *container.Info) string {
	if info.Running() {
		return "running"
	}
	if info.State.Status != "" {
		return info.State.Status
	}
	return "stopped"
}

// printWorkspaceRows renders the list as an aligned table.
func printWorkspaceRows(rows []workspaceRow) {
	if len(rows) == 0 {
		fmt.Println("no devc workspaces found")
		return
	}
	nameW, stateW := len("NAME"), len("STATE")
	for _, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.State) > stateW {
			stateW = len(r.State)
		}
	}
	fmt.Printf("%-*s  %-*s  %s\n", nameW, "NAME", stateW, "STATE", "FOLDER")
	for _, r := range rows {
		fmt.Printf("%-*s  %-*s  %s\n", nameW, r.Name, stateW, r.State, r.Local)
	}
}
