package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/terrakuh/devc/runtime"
)

// Info is the subset of `inspect` output devc needs about a container.
type Info struct {
	ID     string          `json:"Id"`
	Name   string          `json:"Name"`
	State  ContainerState  `json:"State"`
	Config ContainerConfig `json:"Config"`
}

type ContainerState struct {
	Status  string `json:"Status"` // "running", "exited", "created", ...
	Running bool   `json:"Running"`
}

type ContainerConfig struct {
	Labels map[string]string `json:"Labels"`
	Image  string            `json:"Image"`
}

// ImageInfo is the subset of image `inspect` output devc needs: the platform,
// so the agent binary of the right architecture is injected.
type ImageInfo struct {
	Architecture string `json:"Architecture"` // "amd64", "arm64"
	Os           string `json:"Os"`           // "linux"
}

// Running reports whether the container is up.
func (i Info) Running() bool {
	return i.State.Running || strings.EqualFold(i.State.Status, "running")
}

// Find locates the workspace container by its devc name and returns its Info.
// It returns (nil, nil) when no such container exists, distinguishing "absent"
// from a real inspection error.
func Find(ctx context.Context, r runtime.Runner, name string) (*Info, error) {
	var info Info
	err := r.Inspect(ctx, name, &info)
	if err != nil {
		if errors.Is(err, runtime.ErrNoSuchObject) {
			return nil, nil
		}
		return nil, err
	}
	return &info, nil
}

// List returns one Info per workspace devc created, single-container and compose
// alike. Single-container workspaces carry devc's own id label and are found
// directly. Compose service containers are created by compose, not devc, so they
// never carry that label; they are found by their deterministic devc-<id>
// compose project label instead, and a project's several service containers are
// collapsed into one synthesized workspace row carrying only the id label. The
// list is best-effort: a
// container that disappears between the `ps` and its `inspect` is skipped rather
// than failing the whole enumeration.
func List(ctx context.Context, r runtime.Runner) ([]*Info, error) {
	singleIDs, err := psByLabel(ctx, r, LabelID)
	if err != nil {
		return nil, err
	}
	composeIDs, err := psByLabel(ctx, r, LabelComposeProject)
	if err != nil {
		return nil, err
	}
	// One inspect for both sets: every runtime invocation costs tens of
	// milliseconds, which shell completion of -n/--name feels.
	all, err := inspectAll(ctx, r, append(singleIDs, composeIDs...))
	if err != nil {
		return nil, err
	}
	var infos, composeContainers []*Info
	for _, info := range all {
		if info.Config.Labels[LabelID] != "" {
			infos = append(infos, info)
		} else if info.Config.Labels[LabelComposeProject] != "" {
			composeContainers = append(composeContainers, info)
		}
	}

	// Collapse each devc compose project's service containers into a single row,
	// keyed by workspace id. The row reports "running" when any service is up.
	byID := map[string]*Info{}
	for _, c := range composeContainers {
		id, ok := WorkspaceIDFromProject(c.Config.Labels[LabelComposeProject])
		if !ok {
			continue // project not created by devc (e.g. a user COMPOSE_PROJECT_NAME)
		}
		if row, seen := byID[id]; seen {
			if c.Running() && !row.Running() {
				row.State = c.State
			}
			continue
		}
		row := composeRow(id, c)
		byID[id] = row
		infos = append(infos, row)
	}
	return infos, nil
}

// composeRow synthesizes the workspace row for a compose service container. Only
// the id label can be recovered from the container itself (from its project
// name); the display name and the local folder are devc facts compose never
// stored, so callers backfill them - see cmd/devc's listWorkspaces, which reads
// them from the workspace state dir and falls back to NameFromID.
func composeRow(id string, c *Info) *Info {
	return &Info{
		ID:     c.ID,
		State:  c.State,
		Config: ContainerConfig{Labels: map[string]string{LabelID: id}},
	}
}

// listByLabel returns the Info for every container carrying the given label key,
// skipping any that vanish between the `ps` and their `inspect`.
func listByLabel(ctx context.Context, r runtime.Runner, label string) ([]*Info, error) {
	ids, err := psByLabel(ctx, r, label)
	if err != nil {
		return nil, err
	}
	return inspectAll(ctx, r, ids)
}

// psByLabel returns the ids of every container (running or not) carrying label.
func psByLabel(ctx context.Context, r runtime.Runner, label string) ([]string, error) {
	out, err := r.Output(ctx, "ps", "--all", "--filter", "label="+label, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(string(out)), nil
}

// inspectAll inspects the given containers in a single runtime call, one JSON
// object per output line. Duplicate ids are inspected once. A container that
// vanished since it was listed is skipped: podman and docker still print the
// others and only fail with "no such object" for the missing one.
func inspectAll(ctx context.Context, r runtime.Runner, ids []string) ([]*Info, error) {
	seen := map[string]bool{}
	args := []string{"inspect", "--format", "{{json .}}"}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			args = append(args, id)
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	out, err := r.Output(ctx, args...)
	if err != nil && !runtime.IsNoSuchObject(err) {
		return nil, err
	}
	var infos []*Info
	for _, line := range nonEmptyLines(string(out)) {
		if line == "null" {
			continue
		}
		var info Info
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			return nil, fmt.Errorf("decode inspect output: %w", err)
		}
		infos = append(infos, &info)
	}
	return infos, nil
}

// InspectImage returns the platform info for an image reference.
func InspectImage(ctx context.Context, r runtime.Runner, ref string) (*ImageInfo, error) {
	var img ImageInfo
	if err := r.Inspect(ctx, ref, &img); err != nil {
		return nil, err
	}
	return &img, nil
}
