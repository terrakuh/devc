package container

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
)

func imageSpec() *config.Spec {
	return &config.Spec{
		ID:                       "demo-1a2b3c4d",
		Name:                     "demo",
		Kind:                     config.KindImage,
		LocalWorkspaceFolder:     "/home/me/proj",
		ContainerWorkspaceFolder: "/workspaces/proj",
		RemoteUser:               "vscode",
		OverrideCommand:          true,
		Image: &config.ImageSpec{
			Image:        "fedora:44",
			ContainerEnv: map[string]string{"FOO": "bar", "BAZ": "qux"},
			AppPorts:     []config.PortForward{{HostPort: 8080, ContainerPort: 80}},
			Init:         true,
		},
	}
}

// argHas reports whether flag appears immediately followed by value.
func argHas(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestRunArgsImage(t *testing.T) {
	spec := imageSpec()
	args, err := RunArgs(spec, CreateOptions{Userns: "keep-id", SELinuxLabel: "z"}, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")

	if args[0] != "run" || !slices.Contains(args, "--detach") {
		t.Fatalf("not a detached run: %v", args)
	}
	if !argHas(args, "--name", "devc-demo-1a2b3c4d") {
		t.Fatalf("bad name: %s", joined)
	}
	if !argHas(args, "--label", LabelID+"=demo-1a2b3c4d") {
		t.Fatalf("missing id label: %s", joined)
	}
	if !argHas(args, "--userns", "keep-id") {
		t.Fatalf("missing userns: %s", joined)
	}
	if !argHas(args, "--volume", "/home/me/proj:/workspaces/proj:z") {
		t.Fatalf("bad workspace mount: %s", joined)
	}
	if !argHas(args, "--workdir", "/workspaces/proj") {
		t.Fatalf("missing workdir: %s", joined)
	}
	// env is emitted in sorted key order
	if !argHas(args, "--env", "BAZ=qux") || !argHas(args, "--env", "FOO=bar") {
		t.Fatalf("missing env: %s", joined)
	}
	if strings.Index(joined, "BAZ=qux") > strings.Index(joined, "FOO=bar") {
		t.Fatalf("env not sorted: %s", joined)
	}
	if !slices.Contains(args, "--init") {
		t.Fatalf("missing --init: %s", joined)
	}
	if !argHas(args, "--publish", "8080:80") {
		t.Fatalf("missing publish: %s", joined)
	}
	if !argHas(args, "--platform", "linux/amd64") {
		t.Fatalf("missing platform: %s", joined)
	}
	// overrideCommand: entrypoint override before image, keep-alive after.
	if !argHas(args, "--entrypoint", keepAliveEntrypoint) {
		t.Fatalf("missing entrypoint override: %s", joined)
	}
	imgIdx := slices.Index(args, "fedora:44")
	entIdx := slices.Index(args, keepAliveEntrypoint)
	if imgIdx < 0 || entIdx < 0 || entIdx > imgIdx {
		t.Fatalf("entrypoint must precede image: %s", joined)
	}
	if args[len(args)-1] != keepAliveArgs[len(keepAliveArgs)-1] {
		t.Fatalf("keep-alive args must come last: %s", joined)
	}
}

func TestRunArgsNoOverrideNoSELinux(t *testing.T) {
	spec := imageSpec()
	spec.OverrideCommand = false
	args, err := RunArgs(spec, CreateOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if slices.Contains(args, "--entrypoint") {
		t.Fatalf("should not override entrypoint: %s", joined)
	}
	if strings.Contains(joined, ":z") {
		t.Fatalf("should not relabel without SELinuxLabel: %s", joined)
	}
	if !argHas(args, "--volume", "/home/me/proj:/workspaces/proj") {
		t.Fatalf("bad mount: %s", joined)
	}
	if args[len(args)-1] != "fedora:44" {
		t.Fatalf("image must be last with no override command: %s", joined)
	}
}

func TestRunArgsWorkspaceMountOverride(t *testing.T) {
	spec := imageSpec()
	spec.Image.WorkspaceMount = "type=bind,source=/a,target=/b"
	args, _ := RunArgs(spec, CreateOptions{SELinuxLabel: "z"}, "")
	joined := strings.Join(args, " ")
	if !argHas(args, "--mount", "type=bind,source=/a,target=/b") {
		t.Fatalf("explicit workspaceMount not used: %s", joined)
	}
	if strings.Contains(joined, "--volume") {
		t.Fatalf("default -v should be suppressed by workspaceMount: %s", joined)
	}
}

func TestBuildArgs(t *testing.T) {
	spec := &config.Spec{
		ID:   "b-1",
		Kind: config.KindBuild,
		Image: &config.ImageSpec{
			Build: &config.BuildSpec{
				Dockerfile: "/proj/.devcontainer/Containerfile",
				Context:    "/proj/.devcontainer",
				Args:       map[string]string{"A": "1", "B": "2"},
				Target:     "dev",
			},
		},
	}
	args, err := BuildArgs(spec, "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if args[0] != "build" {
		t.Fatalf("not build: %s", joined)
	}
	if !argHas(args, "--tag", "devc/b-1:latest") {
		t.Fatalf("bad tag: %s", joined)
	}
	if !argHas(args, "--file", "/proj/.devcontainer/Containerfile") {
		t.Fatalf("bad file: %s", joined)
	}
	if !argHas(args, "--target", "dev") {
		t.Fatalf("bad target: %s", joined)
	}
	if !argHas(args, "--platform", "linux/arm64") {
		t.Fatalf("bad platform: %s", joined)
	}
	if !argHas(args, "--build-arg", "A=1") || !argHas(args, "--build-arg", "B=2") {
		t.Fatalf("bad build args: %s", joined)
	}
	if args[len(args)-1] != "/proj/.devcontainer" {
		t.Fatalf("context must be last: %s", joined)
	}
}

func TestExecArgs(t *testing.T) {
	args := ExecArgs("devc-x", "vscode", "/workspaces/proj", map[string]string{"K": "v"}, true, false, []string{"go", "version"})
	joined := strings.Join(args, " ")
	if args[0] != "exec" {
		t.Fatalf("not exec: %s", joined)
	}
	if !slices.Contains(args, "--interactive") {
		t.Fatalf("missing interactive: %s", joined)
	}
	if slices.Contains(args, "--tty") {
		t.Fatalf("tty should be absent: %s", joined)
	}
	if !argHas(args, "--user", "vscode") || !argHas(args, "--workdir", "/workspaces/proj") {
		t.Fatalf("missing user/workdir: %s", joined)
	}
	if !argHas(args, "--env", "K=v") {
		t.Fatalf("missing env: %s", joined)
	}
	// container name then command, in order, at the end
	nameIdx := slices.Index(args, "devc-x")
	if nameIdx < 0 || args[nameIdx+1] != "go" || args[nameIdx+2] != "version" {
		t.Fatalf("command must follow container name: %s", joined)
	}
}

func TestFindReturnsNilWhenAbsent(t *testing.T) {
	f := runtime.NewFake() // no OutputFunc => Inspect reports ErrNoSuchObject
	info, err := Find(context.Background(), f, "devc-missing")
	if err != nil {
		t.Fatalf("expected nil error for absent container, got %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info, got %+v", info)
	}
}

func TestFindDecodesRunning(t *testing.T) {
	f := runtime.NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		info := Info{
			ID:    "abc123",
			State: ContainerState{Status: "running", Running: true},
			Config: ContainerConfig{
				Labels: map[string]string{LabelID: "demo-1a2b3c4d"},
			},
		}
		return json.Marshal(info)
	}
	info, err := Find(context.Background(), f, "devc-demo")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || !info.Running() || info.ID != "abc123" {
		t.Fatalf("bad info: %+v", info)
	}
}

func TestDecide(t *testing.T) {
	spec := imageSpec()
	hash := ConfigHash(spec)
	running := &Info{State: ContainerState{Running: true}, Config: ContainerConfig{Labels: map[string]string{LabelConfigHash: hash}}}
	stopped := &Info{State: ContainerState{Status: "exited"}, Config: ContainerConfig{Labels: map[string]string{LabelConfigHash: hash}}}
	drifted := &Info{State: ContainerState{Running: true}, Config: ContainerConfig{Labels: map[string]string{LabelConfigHash: "old"}}}

	if got := Decide(nil, spec, false); got != ActionCreate {
		t.Fatalf("absent => %v", got)
	}
	if got := Decide(running, spec, false); got != ActionAttach {
		t.Fatalf("running match => %v", got)
	}
	if got := Decide(stopped, spec, false); got != ActionStart {
		t.Fatalf("stopped match => %v", got)
	}
	if got := Decide(drifted, spec, false); got != ActionDrift {
		t.Fatalf("drift no-recreate => %v", got)
	}
	if got := Decide(drifted, spec, true); got != ActionRecreate {
		t.Fatalf("drift recreate => %v", got)
	}
}

func TestConfigHashStableAndSensitive(t *testing.T) {
	a := imageSpec()
	b := imageSpec()
	if ConfigHash(a) != ConfigHash(b) {
		t.Fatal("hash not stable")
	}
	// Location-only changes must not affect the hash.
	b.ID = "different-id"
	b.LocalWorkspaceFolder = "/elsewhere"
	b.ConfigPath = "/elsewhere/.devcontainer.json"
	if ConfigHash(a) != ConfigHash(b) {
		t.Fatal("hash should ignore id/location")
	}
	// A behavioural change must affect it.
	b.Image.Image = "ubuntu:24.04"
	if ConfigHash(a) == ConfigHash(b) {
		t.Fatal("hash should change with image")
	}
}
