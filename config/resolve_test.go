package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a devcontainer.json into a temp .devcontainer dir and
// returns the project folder.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveCompose(t *testing.T) {
	dir := writeConfig(t, `{
		// like the real one
		"name": "Shop",
		"service": "workspace",
		"workspaceFolder": "/workspace",
		"dockerComposeFile": ["compose.yaml", "telemetry.dev.yaml"],
		"forwardPorts": [8080, 5173],
		"customizations": {"vscode": {"extensions": ["golang.go"]}},
	}`)
	spec, warnings, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindCompose {
		t.Fatalf("kind = %s", spec.Kind)
	}
	if spec.Compose == nil || spec.Compose.Service != "workspace" {
		t.Fatalf("compose = %+v", spec.Compose)
	}
	if len(spec.Compose.Files) != 2 {
		t.Fatalf("files = %v", spec.Compose.Files)
	}
	if !filepath.IsAbs(spec.Compose.Files[0]) {
		t.Fatalf("compose file not absolute: %s", spec.Compose.Files[0])
	}
	if spec.OverrideCommand {
		t.Fatal("overrideCommand should default false for compose")
	}
	if spec.ShutdownAction != ShutdownStopCompose {
		t.Fatalf("shutdown = %s", spec.ShutdownAction)
	}
	if len(spec.Ports) != 2 || spec.Ports[0].HostPort != 8080 {
		t.Fatalf("ports = %+v", spec.Ports)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestResolveImage(t *testing.T) {
	dir := writeConfig(t, `{
		"name": "scratch",
		"image": "fedora:44",
		"forwardPorts": ["127.0.0.1:3000", "8080:80"],
		"remoteUser": "vscode",
		"postCreateCommand": "go version",
	}`)
	spec, _, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindImage {
		t.Fatalf("kind = %s", spec.Kind)
	}
	if spec.Image == nil || spec.Image.Image != "fedora:44" {
		t.Fatalf("image = %+v", spec.Image)
	}
	if !spec.OverrideCommand {
		t.Fatal("overrideCommand should default true for single container")
	}
	// default workspaceFolder for single container
	want := "/workspaces/" + filepath.Base(dir)
	if spec.ContainerWorkspaceFolder != want {
		t.Fatalf("workspaceFolder = %s want %s", spec.ContainerWorkspaceFolder, want)
	}
	if spec.Ports[0].HostIP != "127.0.0.1" || spec.Ports[0].ContainerPort != 3000 {
		t.Fatalf("port0 = %+v", spec.Ports[0])
	}
	if spec.Ports[1].HostPort != 8080 || spec.Ports[1].ContainerPort != 80 {
		t.Fatalf("port1 = %+v", spec.Ports[1])
	}
	if spec.Hooks.PostCreate.Kind != CommandShell || spec.Hooks.PostCreate.Shell != "go version" {
		t.Fatalf("postCreate = %+v", spec.Hooks.PostCreate)
	}
}

func TestResolveExpandsMountTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := writeConfig(t, `{
		"name": "mounted",
		"image": "fedora:44",
		"workspaceMount": "type=bind,source=~/project,target=/workspace",
		"mounts": [
			"type=bind,source=~/data,target=/data",
			{"type": "bind", "source": "~", "target": "/home-root"},
			"type=bind,source=/etc/hosts,target=/etc/hosts",
		],
	}`)
	spec, _, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := spec.Image.WorkspaceMount, "type=bind,source="+home+"/project,target=/workspace"; got != want {
		t.Fatalf("workspaceMount = %q want %q", got, want)
	}
	wantMounts := []string{
		"type=bind,source=" + home + "/data,target=/data",
		"type=bind,source=" + home + ",target=/home-root",
		"type=bind,source=/etc/hosts,target=/etc/hosts", // no ~, untouched
	}
	for i, want := range wantMounts {
		if spec.Image.Mounts[i] != want {
			t.Fatalf("mount[%d] = %q want %q", i, spec.Image.Mounts[i], want)
		}
	}
}

func TestResolveBuild(t *testing.T) {
	dir := writeConfig(t, `{
		"name": "built",
		"build": {"dockerfile": "Containerfile", "args": {"FOO": "bar"}},
	}`)
	spec, _, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindBuild {
		t.Fatalf("kind = %s", spec.Kind)
	}
	if spec.Image.Build == nil {
		t.Fatal("build spec nil")
	}
	// dockerfile resolved relative to the .devcontainer dir
	wantDockerfile := filepath.Join(dir, ".devcontainer", "Containerfile")
	if spec.Image.Build.Dockerfile != wantDockerfile {
		t.Fatalf("dockerfile = %s want %s", spec.Image.Build.Dockerfile, wantDockerfile)
	}
	if spec.Image.Build.Args["FOO"] != "bar" {
		t.Fatalf("args = %v", spec.Image.Build.Args)
	}
}

func TestResolveRejections(t *testing.T) {
	cases := map[string]string{
		"features":            `{"image":"x","features":{"ghcr.io/x/y":{}}}`,
		"compose+image":       `{"image":"x","dockerComposeFile":"c.yaml","service":"s","workspaceFolder":"/w"}`,
		"compose no service":  `{"dockerComposeFile":"c.yaml","workspaceFolder":"/w"}`,
		"compose no folder":   `{"dockerComposeFile":"c.yaml","service":"s"}`,
		"nothing":             `{"name":"empty"}`,
		"compose+mounts":      `{"dockerComposeFile":"c.yaml","service":"s","workspaceFolder":"/w","mounts":["type=bind,source=/a,target=/b"]}`,
		"bad shutdown":        `{"image":"x","shutdownAction":"explode"}`,
		"bad port":            `{"image":"x","forwardPorts":[99999]}`,
		"updateRemoteUserUID": `{"image":"x","updateRemoteUserUID":true}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeConfig(t, body)
			if _, _, err := LoadSpec(dir, ""); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestResolveNamedHooksParallel(t *testing.T) {
	dir := writeConfig(t, `{
		"image": "x",
		"postCreateCommand": {"a": "echo a", "b": ["echo", "b"]},
	}`)
	spec, _, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	pc := spec.Hooks.PostCreate
	if pc.Kind != CommandNamed || len(pc.Named) != 2 {
		t.Fatalf("postCreate = %+v", pc)
	}
	if pc.Named["a"].Shell != "echo a" || pc.Named["b"].Argv[0] != "echo" {
		t.Fatalf("named = %+v", pc.Named)
	}
}

func TestWorkspaceIDStable(t *testing.T) {
	a := WorkspaceID("Shop", "/home/x/workspace")
	b := WorkspaceID("Shop", "/home/x/workspace")
	c := WorkspaceID("Shop", "/home/x/other")
	if a != b {
		t.Fatal("id not stable")
	}
	if a == c {
		t.Fatal("id should differ by folder")
	}
	if len(a) != len("shop-")+8 {
		t.Fatalf("id shape = %q", a)
	}
}
