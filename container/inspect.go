package container

import (
	"context"
	"errors"
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

// List returns the Info for every container devc created (single-container and
// compose alike), found by the devc id label. The list is best-effort: a
// container that disappears between the `ps` and its `inspect` is skipped rather
// than failing the whole enumeration.
func List(ctx context.Context, r runtime.Runner) ([]*Info, error) {
	out, err := r.Output(ctx, "ps", "--all", "--filter", "label="+LabelID, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	var infos []*Info
	for _, id := range nonEmptyLines(string(out)) {
		info, err := Find(ctx, r, id)
		if err != nil || info == nil {
			continue
		}
		infos = append(infos, info)
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
