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

func TestComposeUpArgs(t *testing.T) {
	spec := composeSpec()
	args := ComposeUpArgs(spec, "devc-shop-c52ddf65")

	// project name and both files, files in overlay order, before the verb.
	assert.Equal(t, []string{
		"--project-name", "devc-shop-c52ddf65",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"up", "--detach",
	}, args)
}

func TestComposeUpArgsRunServices(t *testing.T) {
	spec := composeSpec()
	spec.Compose.RunServices = []string{"workspace", "db"}
	args := ComposeUpArgs(spec, "p")
	assert.Equal(t, []string{"workspace", "db"}, args[len(args)-2:])
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

func TestComposeRestartArgs(t *testing.T) {
	spec := composeSpec()

	// Default: only the attach service, after the verb.
	assert.Equal(t, []string{
		"--project-name", "p",
		"--file", "/w/.devcontainer/compose.yaml",
		"--file", "/w/.devcontainer/telemetry.dev.yaml",
		"restart", "workspace",
	}, ComposeRestartArgs(spec, "p", false))

	// --all with RunServices restarts exactly those services.
	spec.Compose.RunServices = []string{"workspace", "db"}
	all := ComposeRestartArgs(spec, "p", true)
	assert.Equal(t, "restart", all[len(all)-3])
	assert.Equal(t, []string{"workspace", "db"}, all[len(all)-2:])

	// --all with no RunServices restarts everything (no service args).
	spec.Compose.RunServices = nil
	assert.Equal(t, "restart", ComposeRestartArgs(spec, "p", true)[len(ComposeRestartArgs(spec, "p", true))-1])
}

func TestComposeLogsArgs(t *testing.T) {
	spec := composeSpec()
	args := ComposeLogsArgs(spec, "p", true, "db")
	assert.Contains(t, args, "logs")
	assert.Contains(t, args, "--follow")
	assert.Equal(t, "db", args[len(args)-1])

	noSvc := ComposeLogsArgs(spec, "p", false, "")
	assert.Equal(t, "logs", noSvc[len(noSvc)-1], "no service, no follow => logs is last")
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
