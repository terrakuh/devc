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
	"github.com/terrakuh/devc/state"
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
	infos, err := listWorkspaces(ctx, r)
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

// listWorkspaces enumerates every workspace devc created, backfilling the facts
// a compose workspace's containers cannot carry: compose creates them, so they
// have no devc name/folder label and container.List can only recover the id. The
// values `up` recorded in the workspace state dir stand in, which is what makes
// a compose workspace show its folder here and be addressable by -n/--name.
// Labels are never overwritten: a live container is the better source.
func listWorkspaces(ctx context.Context, r runtime.Runner) ([]*container.Info, error) {
	infos, err := container.List(ctx, r)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		backfillFromState(info)
	}
	return infos, nil
}

// backfillFromState fills an Info's missing devc name/folder labels from the
// workspace's state dir. Best-effort: an unreadable or absent state file just
// leaves the name derived from the id and the folder blank, so a workspace whose
// state predates this bookkeeping still lists (it regains its folder on the next
// `devc up`).
func backfillFromState(info *container.Info) {
	labels := info.Config.Labels
	id := labels[container.LabelID]
	if id == "" || (labels[container.LabelName] != "" && labels[container.LabelLocal] != "") {
		return
	}
	s, err := state.Peek(id)
	if err != nil {
		s = nil
	}
	if labels[container.LabelName] == "" {
		if s != nil && s.Name != "" {
			labels[container.LabelName] = s.Name
		} else {
			labels[container.LabelName] = container.NameFromID(id)
		}
	}
	if labels[container.LabelLocal] == "" && s != nil {
		labels[container.LabelLocal] = s.LocalFolder
	}
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
