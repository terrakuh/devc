package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
)

// serviceRow is one line of `devc ps` (also the --json element). It describes
// one container of the current workspace, or - for a compose service the files
// declare but nothing has created yet - the absence of one.
type serviceRow struct {
	Service   string `json:"service"`
	State     string `json:"state"`
	Container string `json:"containerID,omitempty"`
	Image     string `json:"image,omitempty"`
	// Workspace marks the service devc attaches to: the one `exec`, `ssh` and
	// the editor land in.
	Workspace bool `json:"workspace"`
}

// runPs implements `devc ps`: the containers of *this* workspace, service by
// service. `devc list` answers "which workspaces exist on this host"; ps answers
// "what is this workspace made of, and what is up right now".
func runPs(args []string) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	jsonOut := fs.Bool("json", false, "emit the rows as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}
	services, err := composeServices(e, fs.Args())
	if err != nil {
		return err
	}

	rows, err := psRows(ctx, e, services)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printServiceRows(rows, e.spec.Kind == config.KindCompose)
	return nil
}

// psRows collects one row per container of the workspace, narrowed to services
// when the caller named any.
func psRows(ctx context.Context, e *env, services []string) ([]serviceRow, error) {
	var rows []serviceRow
	if e.spec.Kind == config.KindCompose {
		var err error
		if rows, err = composeRows(ctx, e); err != nil {
			return nil, err
		}
	} else {
		row := serviceRow{Service: e.spec.Name, State: "not created", Workspace: true}
		_, info, err := e.attachRef(ctx)
		if err != nil {
			return nil, err
		}
		if info != nil {
			row.State = containerState(info)
			row.Container = short(info.ID)
			row.Image = info.Config.Image
		}
		rows = []serviceRow{row}
	}
	return filterRows(rows, services)
}

// composeRows lists the project's containers and rounds them out with the
// services the compose files declare but that have no container at all, so a
// service that was never started is visible rather than simply missing.
func composeRows(ctx context.Context, e *env) ([]serviceRow, error) {
	project := container.ProjectName(e.spec)
	infos, err := container.ListComposeProject(ctx, e.runner, project)
	if err != nil {
		return nil, err
	}

	rows := make([]serviceRow, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		svc := container.ServiceOf(info)
		seen[svc] = true
		rows = append(rows, serviceRow{
			Service:   svc,
			State:     containerState(info),
			Container: short(info.ID),
			Image:     info.Config.Image,
			Workspace: svc == e.spec.Compose.Service,
		})
	}
	for _, svc := range declaredServices(ctx, e, project) {
		if !seen[svc] {
			rows = append(rows, serviceRow{
				Service:   svc,
				State:     "not created",
				Workspace: svc == e.spec.Compose.Service,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Service < rows[j].Service })
	return rows, nil
}

// declaredServices asks compose which services the files define. It is
// best-effort: on failure ps still lists the containers that exist, which is the
// part that matters, so a compose implementation that cannot answer degrades the
// output instead of failing the command.
func declaredServices(ctx context.Context, e *env, project string) []string {
	comp, err := e.composeImpl(ctx)
	if err == nil {
		var names []string
		if names, err = container.ComposeServiceNames(ctx, comp, e.spec, project); err == nil {
			return names
		}
	}
	if !e.flags.quiet {
		fmt.Fprintf(os.Stderr, "warning: could not list the project's services, showing only existing containers: %v\n", err)
	}
	return nil
}

// filterRows narrows the rows to the named services, rejecting a name that
// matches nothing - a typo should not read as "that service is down".
func filterRows(rows []serviceRow, services []string) ([]serviceRow, error) {
	if len(services) == 0 {
		return rows, nil
	}
	var out []serviceRow
	for _, want := range services {
		found := false
		for _, r := range rows {
			if r.Service == want {
				out = append(out, r)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no service %q in this workspace (have %s)", want, strings.Join(serviceNames(rows), ", "))
		}
	}
	return out, nil
}

func serviceNames(rows []serviceRow) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Service)
	}
	return names
}

// printServiceRows renders the rows as an aligned table, marking the workspace's
// own service with a star (compose only - a single-container workspace has just
// the one row, so the mark would say nothing).
func printServiceRows(rows []serviceRow, compose bool) {
	if len(rows) == 0 {
		fmt.Println("no containers")
		return
	}
	const (
		svcHead = "SERVICE"
		ctrHead = "CONTAINER"
	)
	svcW, stateW, ctrW := len(svcHead), len("STATE"), len(ctrHead)
	name := func(r serviceRow) string {
		if compose && r.Workspace {
			return r.Service + "*"
		}
		return r.Service
	}
	for _, r := range rows {
		svcW = max(svcW, len(name(r)))
		stateW = max(stateW, len(r.State))
		ctrW = max(ctrW, len(r.Container))
	}

	fmt.Printf("%-*s  %-*s  %-*s  %s\n", svcW, svcHead, stateW, "STATE", ctrW, ctrHead, "IMAGE")
	starred := false
	for _, r := range rows {
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", svcW, name(r), stateW, r.State, ctrW, dash(r.Container), dash(r.Image))
		starred = starred || name(r) != r.Service
	}
	// Only explain the star when one was actually printed; `devc ps db` narrows
	// the table to rows that may not include the workspace's own service.
	if starred {
		fmt.Println("\n* the workspace service devc attaches to")
	}
}

// dash renders an empty cell as "-" so a missing container reads as absent
// rather than as a formatting glitch.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
