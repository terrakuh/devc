package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/sshconf"
	"github.com/terrakuh/devc/state"
)

// workspaceRef is what retireRenamed knows about some workspace on the host: its
// id and the folder and config it was brought up from.
type workspaceRef struct {
	ID         string
	Name       string
	Local      string
	ConfigPath string // empty when recorded by a devc that did not track it
}

// renamedFrom picks the workspaces that are earlier incarnations of spec under a
// different name. The id embeds the name, so renaming a devcontainer.json gives
// it a new id and orphans the old container, keys and ssh block; those are what
// this finds. A workspace is the same one when it was brought up from the same
// folder with the same config. Refs recorded before the config path was tracked
// fall back to the folder alone.
func renamedFrom(spec *config.Spec, refs []workspaceRef) []workspaceRef {
	seen := map[string]bool{spec.ID: true}
	var out []workspaceRef
	for _, r := range refs {
		if seen[r.ID] || r.Local == "" || r.Local != spec.LocalWorkspaceFolder {
			continue
		}
		if r.ConfigPath != "" && r.ConfigPath != spec.ConfigPath {
			continue // another config in the same folder: a distinct workspace
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// knownWorkspaces gathers every workspace devc can see, from containers and
// from state dirs, so one whose container is already gone still has its keys
// and ssh block found.
func knownWorkspaces(ctx context.Context, r runtime.Runner) []workspaceRef {
	byID := map[string]*workspaceRef{}
	if states, err := state.List(); err == nil {
		for _, s := range states {
			byID[s.ID] = &workspaceRef{ID: s.ID, Name: s.Name, Local: s.LocalFolder, ConfigPath: s.ConfigPath}
		}
	}
	if infos, err := listWorkspaces(ctx, r); err == nil {
		for _, info := range infos {
			l := info.Config.Labels
			id := l[container.LabelID]
			if id == "" {
				continue
			}
			ref := byID[id]
			if ref == nil {
				ref = &workspaceRef{ID: id}
				byID[id] = ref
			}
			// A live container's labels beat the state file.
			if v := l[container.LabelName]; v != "" {
				ref.Name = v
			}
			if v := l[container.LabelLocal]; v != "" {
				ref.Local = v
			}
			if v := l[container.LabelConfig]; v != "" {
				ref.ConfigPath = v
			}
		}
	}
	refs := make([]workspaceRef, 0, len(byID))
	for _, ref := range byID {
		refs = append(refs, *ref)
	}
	return refs
}

// retireRenamed removes every earlier incarnation of e's workspace under a
// previous name: its container(s), state dir, control sockets and ssh block.
// Best-effort - a failure is reported but never blocks bringing the current
// workspace up.
func retireRenamed(ctx context.Context, e *env) {
	for _, old := range renamedFrom(e.spec, knownWorkspaces(ctx, e.runner)) {
		fmt.Printf("removing %q: workspace was renamed to %q\n", old.Name, e.spec.Name)
		if err := removeWorkspaceContainers(ctx, e, old.ID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove containers of %q: %v\n", old.Name, err)
			continue // keep its keys and ssh block so it stays reachable
		}
		if dir, err := state.For(old.ID); err == nil {
			if err := dir.Remove(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not remove state dir of %q: %v\n", old.Name, err)
			}
		}
		if err := removeControlDir(old.ID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove control socket dir of %q: %v\n", old.Name, err)
		}
		if err := sshconf.RemoveWorkspaceConfig(devcSSHConfigPath(old.ID)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove ssh config of %q: %v\n", old.Name, err)
		}
	}
}

// removeWorkspaceContainers removes the single container and/or compose project
// devc created for workspace id. The compose project is torn down with the
// current spec's compose files when it is a compose workspace (a rename leaves
// them unchanged), which also drops the project network; otherwise its service
// containers are removed directly.
func removeWorkspaceContainers(ctx context.Context, e *env, id string) error {
	if err := container.Remove(ctx, e.runner, "devc-"+id); err != nil {
		if existing, ferr := container.Find(ctx, e.runner, "devc-"+id); ferr != nil || existing != nil {
			return err
		}
	}

	project := "devc-" + id
	members, err := container.ListComposeProject(ctx, e.runner, project)
	if err != nil || len(members) == 0 {
		return err
	}
	if e.spec.Kind == config.KindCompose {
		if comp, err := e.composeImpl(ctx); err == nil {
			io := runtime.IO{Stdout: os.Stdout, Stderr: os.Stderr}
			if err := container.ComposeDown(ctx, comp, e.spec, project, false, io); err == nil {
				return nil
			}
		}
	}
	for _, m := range members {
		if err := container.Remove(ctx, e.runner, m.ID); err != nil {
			return err
		}
	}
	return nil
}
