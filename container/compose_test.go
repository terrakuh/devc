package container

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func composeSpec() *config.Spec {
	return &config.Spec{
		ID:   "shop-c52ddf65",
		Name: "shop",
		Kind: config.KindCompose,
		Compose: &config.ComposeSpec{
			Files:   []string{"/w/.devcontainer/compose.yaml", "/w/.devcontainer/telemetry.dev.yaml"},
			Service: "workspace",
		},
	}
}

func TestProjectName(t *testing.T) {
	spec := composeSpec()
	assert.Equal(t, "devc-shop-c52ddf65", ProjectName(spec))

	t.Setenv("COMPOSE_PROJECT_NAME", "custom")
	assert.Equal(t, "custom", ProjectName(spec), "COMPOSE_PROJECT_NAME must win")
}

func TestWorkspaceIDFromProject(t *testing.T) {
	// Round-trips ProjectName's default form, including hyphenated names.
	id, ok := WorkspaceIDFromProject("devc-my-shop-c52ddf65")
	assert.True(t, ok)
	assert.Equal(t, "my-shop-c52ddf65", id)
	assert.Equal(t, "my-shop", NameFromID(id))

	// A project not named by devc (e.g. a user COMPOSE_PROJECT_NAME) is rejected.
	_, ok = WorkspaceIDFromProject("custom")
	assert.False(t, ok)
	_, ok = WorkspaceIDFromProject("devc-")
	assert.False(t, ok)
}

func TestComposeTargets(t *testing.T) {
	// An explicit list wins over the fallback; no explicit list falls back.
	assert.Equal(t, []string{"db"}, ComposeTargets([]string{"db"}, []string{"workspace"}))
	assert.Equal(t, []string{"workspace"}, ComposeTargets(nil, []string{"workspace"}))
	assert.Empty(t, ComposeTargets(nil, nil))

	// An empty target list means "every service", so it covers anything.
	assert.True(t, ComposeCovers(nil, "workspace"))
	assert.True(t, ComposeCovers([]string{"db", "workspace"}, "workspace"))
	assert.False(t, ComposeCovers([]string{"db"}, "workspace"))
}

func TestComposeUpArgs(t *testing.T) {
	spec := composeSpec()
	args := ComposeUpArgs(spec, "devc-shop-c52ddf65", false, false, nil)

	// project name and both files, files in overlay order, before the verb.
	assert.Equal(t, []string{
		"--project-name", "devc-shop-c52ddf65",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"up", "--detach",
	}, args)
}

func TestComposeUpArgsRebuild(t *testing.T) {
	spec := composeSpec()

	// --rebuild adds --build and, because a rebuild implies a recreate,
	// --force-recreate as well.
	rebuilt := ComposeUpArgs(spec, "p", true, false, nil)
	assert.Equal(t, []string{"up", "--detach", "--build", "--force-recreate"}, rebuilt[len(rebuilt)-4:])

	// --recreate alone forces recreation without rebuilding the image.
	recreated := ComposeUpArgs(spec, "p", false, true, nil)
	assert.Equal(t, []string{"up", "--detach", "--force-recreate"}, recreated[len(recreated)-3:])
	assert.NotContains(t, recreated, "--build")
}

func TestComposeUpArgsServices(t *testing.T) {
	spec := composeSpec()

	// Services land after the verb and its flags, in the given order.
	args := ComposeUpArgs(spec, "p", false, false, []string{"workspace", "db"})
	assert.Equal(t, []string{"workspace", "db"}, args[len(args)-2:])

	// Rebuilding a single service keeps the flags in front of it.
	one := ComposeUpArgs(spec, "p", true, false, []string{"db"})
	assert.Equal(t, []string{"up", "--detach", "--build", "--force-recreate", "db"}, one[len(one)-5:])
}

func TestComposeDownArgs(t *testing.T) {
	spec := composeSpec()
	assert.Equal(t, []string{
		"--project-name", "p",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"down",
	}, ComposeDownArgs(spec, "p", false))

	withVols := ComposeDownArgs(spec, "p", true)
	assert.Equal(t, "--volumes", withVols[len(withVols)-1])
}

func TestComposeRemoveArgs(t *testing.T) {
	spec := composeSpec()

	// Per-service teardown stops the containers before removing them, and never
	// touches the project's network or volumes.
	args := ComposeRemoveArgs(spec, "p", []string{"db", "cache"})
	assert.Equal(t, []string{
		"--project-name", "p",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"rm", "--force", "--stop", "db", "cache",
	}, args)
	assert.NotContains(t, args, "--volumes")
}

func TestComposeStopArgs(t *testing.T) {
	spec := composeSpec()

	// No services: the whole project stops.
	assert.Equal(t, "stop", ComposeStopArgs(spec, "p", nil)[6])

	named := ComposeStopArgs(spec, "p", []string{"db"})
	assert.Equal(t, []string{"stop", "db"}, named[len(named)-2:])
}

func TestComposeRestartArgs(t *testing.T) {
	spec := composeSpec()

	// The services come straight after the verb; callers resolve their default
	// (attach service, runServices, ...) through ComposeTargets.
	assert.Equal(t, []string{
		"--project-name", "p",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"restart", "workspace",
	}, ComposeRestartArgs(spec, "p", []string{"workspace"}))

	multi := ComposeRestartArgs(spec, "p", []string{"workspace", "db"})
	assert.Equal(t, []string{"restart", "workspace", "db"}, multi[len(multi)-3:])

	// No services restarts everything (no service args).
	empty := ComposeRestartArgs(spec, "p", nil)
	assert.Equal(t, "restart", empty[len(empty)-1])
}

func TestComposeLogsArgs(t *testing.T) {
	spec := composeSpec()
	args := ComposeLogsArgs(spec, "p", true, []string{"db"})
	assert.Contains(t, args, "logs")
	assert.Contains(t, args, "--follow")
	assert.Equal(t, "db", args[len(args)-1])

	multi := ComposeLogsArgs(spec, "p", false, []string{"db", "cache"})
	assert.Equal(t, []string{"logs", "db", "cache"}, multi[len(multi)-3:])

	noSvc := ComposeLogsArgs(spec, "p", false, nil)
	assert.Equal(t, "logs", noSvc[len(noSvc)-1], "no service, no follow => logs is last")
}

func TestComposeConfigArgs(t *testing.T) {
	spec := composeSpec()
	assert.Equal(t, []string{
		"--project-name", "p",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"config", "--services",
	}, ComposeConfigArgs(spec, "p"))
}

func TestListComposeProject(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		switch args[0] {
		case "ps":
			// The filter pins the project, so another project's containers - and
			// devc's own single-container workspaces - stay out of the listing.
			assert.Contains(t, args, "label="+LabelComposeProject+"=p")
			return []byte("ctr-b\nctr-a\n"), nil
		case "inspect":
			svc := map[string]string{"ctr-a": "workspace", "ctr-b": "db"}[args[len(args)-1]]
			return json.Marshal(Info{
				ID:     args[len(args)-1],
				Config: ContainerConfig{Labels: map[string]string{LabelComposeService: svc}},
			})
		}
		return nil, nil
	}

	infos, err := ListComposeProject(context.Background(), f, "p")
	require.NoError(t, err)
	require.Len(t, infos, 2)
	// Sorted by service name, not by the order `ps` happened to report.
	assert.Equal(t, "db", ServiceOf(infos[0]))
	assert.Equal(t, "workspace", ServiceOf(infos[1]))
}

func TestFindComposeServiceOne(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		switch args[0] {
		case "ps":
			// filters must include both project and service labels
			assert.Contains(t, args, "label="+LabelComposeProject+"=devc-shop-c52ddf65")
			assert.Contains(t, args, "label="+LabelComposeService+"=workspace")
			return []byte("ctr-123\n"), nil
		case "inspect":
			info := Info{ID: "ctr-123", State: ContainerState{Running: true}}
			return json.Marshal(info)
		}
		return nil, nil
	}
	info, err := FindComposeService(context.Background(), f, "devc-shop-c52ddf65", "workspace")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "ctr-123", info.ID)
	assert.True(t, info.Running())
}

func TestFindComposeServiceNone(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) { return []byte("\n"), nil }
	info, err := FindComposeService(context.Background(), f, "p", "svc")
	require.NoError(t, err)
	assert.Nil(t, info, "no container => nil, nil")
}

func TestFindComposeServiceScaled(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) { return []byte("a\nb\n"), nil }
	_, err := FindComposeService(context.Background(), f, "p", "svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
