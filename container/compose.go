package container

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
)

// Compose label keys set by every compose implementation on the containers it
// creates. devc locates the attach container by these, never by generating an
// override file.
const (
	LabelComposeProject = "com.docker.compose.project"
	LabelComposeService = "com.docker.compose.service"
)

// projectPrefix is the fixed prefix of a devc-derived compose project name.
const projectPrefix = "devc-"

// ProjectName returns the compose project name for a workspace. A user-set
// COMPOSE_PROJECT_NAME wins (compose itself would honour it, so devc must agree
// to find the containers); otherwise it is derived from the workspace id.
func ProjectName(spec *config.Spec) string {
	if p := os.Getenv("COMPOSE_PROJECT_NAME"); p != "" {
		return p
	}
	return projectPrefix + spec.ID
}

// WorkspaceIDFromProject recovers the workspace id from a compose project name
// that ProjectName produced (devc-<id>), reporting false for any project not
// named that way. It is the inverse used by List to attribute compose service
// containers - which never carry devc's own labels - back to a workspace.
// Projects created under a user-set COMPOSE_PROJECT_NAME are not recognized,
// matching ProjectName: devc can only find what it named deterministically.
func WorkspaceIDFromProject(project string) (string, bool) {
	id, ok := strings.CutPrefix(project, projectPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// fileFlags renders the -f flags for a compose spec's files, in overlay order.
func fileFlags(spec *config.Spec) []string {
	args := make([]string, 0, len(spec.Compose.Files)*2)
	for _, f := range spec.Compose.Files {
		args = append(args, "--file", f)
	}
	return args
}

// baseArgs is the `-p <project> -f a -f b` prefix common to every compose verb.
func baseArgs(spec *config.Spec, project string) []string {
	args := []string{"--project-name", project}
	return append(args, fileFlags(spec)...)
}

// ComposeTargets picks the services a compose verb acts on: the ones the user
// named explicitly, or fallback when they named none. An empty result means
// "every service in the project" - that is how compose reads a missing service
// list, so it needs no special casing at the call sites.
func ComposeTargets(explicit, fallback []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	return fallback
}

// ComposeCovers reports whether a target list includes service, treating the
// empty list as "every service" (see ComposeTargets).
func ComposeCovers(targets []string, service string) bool {
	return len(targets) == 0 || slices.Contains(targets, service)
}

// ComposeUpArgs constructs `compose -p <project> -f <file> up -d [--build]
// [--force-recreate] [services]`. rebuild adds --build so the image is rebuilt
// from source; recreate adds --force-recreate so the container is replaced even
// when compose considers it up-to-date. A rebuild implies a recreate: a running
// container keeps its old image until it is recreated, so a bare --build would
// rebuild the image but leave the stale container in place. An empty services
// list brings up every service in the project.
func ComposeUpArgs(spec *config.Spec, project string, rebuild, recreate bool, services []string) []string {
	args := baseArgs(spec, project)
	args = append(args, "up", "--detach")
	if rebuild {
		args = append(args, "--build")
	}
	if recreate || rebuild {
		args = append(args, "--force-recreate")
	}
	return append(args, services...)
}

// ComposeDownArgs constructs `compose -p <project> -f <file> down [--volumes]`,
// which tears the whole project down. Removing individual services goes through
// ComposeRemoveArgs instead.
func ComposeDownArgs(spec *config.Spec, project string, volumes bool) []string {
	args := baseArgs(spec, project)
	args = append(args, "down")
	if volumes {
		args = append(args, "--volumes")
	}
	return args
}

// ComposeRemoveArgs constructs `compose -p <project> -f <file> rm --force --stop
// <services>`, the per-service counterpart of down: it stops and removes just
// those containers and leaves the project's network and named volumes alone.
// `down <services>` would be the obvious spelling but is a recent docker-compose
// addition that podman-compose does not share, whereas `rm` is universal.
func ComposeRemoveArgs(spec *config.Spec, project string, services []string) []string {
	args := append(baseArgs(spec, project), "rm", "--force", "--stop")
	return append(args, services...)
}

// ComposeStopArgs constructs `compose -p <project> -f <file> stop [services]`.
// An empty services list stops the whole project.
func ComposeStopArgs(spec *config.Spec, project string, services []string) []string {
	return append(append(baseArgs(spec, project), "stop"), services...)
}

// ComposeRestartArgs constructs `compose -p <project> -f <file> restart
// [services]`. An empty services list restarts every service in the project;
// callers resolve their default through ComposeTargets.
func ComposeRestartArgs(spec *config.Spec, project string, services []string) []string {
	return append(append(baseArgs(spec, project), "restart"), services...)
}

// ComposeLogsArgs constructs `compose -p <project> -f <file> logs [--follow]
// [services]`. An empty services list shows the whole project's logs.
func ComposeLogsArgs(spec *config.Spec, project string, follow bool, services []string) []string {
	args := baseArgs(spec, project)
	args = append(args, "logs")
	if follow {
		args = append(args, "--follow")
	}
	return append(args, services...)
}

// ComposeUp brings the given services up (detached); an empty list means every
// service in the project. rebuild rebuilds the service images before
// (re)creating; recreate forces the containers to be replaced even when compose
// considers them up-to-date.
func ComposeUp(ctx context.Context, c *runtime.Compose, spec *config.Spec, project string, rebuild, recreate bool, services []string, io runtime.IO) error {
	return c.Run(ctx, ComposeUpArgs(spec, project, rebuild, recreate, services), io)
}

// ComposeDown tears the project down.
func ComposeDown(ctx context.Context, c *runtime.Compose, spec *config.Spec, project string, volumes bool, io runtime.IO) error {
	return c.Run(ctx, ComposeDownArgs(spec, project, volumes), io)
}

// FindComposeService locates the single container backing spec.Service in the
// given project, using the container runtime's `ps` filtered by compose labels.
// It returns (nil, nil) when the service has no container, and an error when
// the service is scaled to more than one.
func FindComposeService(ctx context.Context, r runtime.Runner, project, service string) (*Info, error) {
	out, err := r.Output(ctx, "ps", "--all",
		"--filter", "label="+LabelComposeProject+"="+project,
		"--filter", "label="+LabelComposeService+"="+service,
		"--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := nonEmptyLines(string(out))
	switch len(ids) {
	case 0:
		return nil, nil
	case 1:
		return Find(ctx, r, ids[0])
	default:
		return nil, fmt.Errorf("service %q has %d containers in project %q; devc needs exactly one (is the service scaled?)", service, len(ids), project)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}
