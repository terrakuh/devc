package container

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
)

// keepAliveEntrypoint and keepAliveArgs hold a container alive when
// overrideCommand is set. `sleep infinity` is avoided because it is not POSIX
// and busybox/dash images vary; an unbounded shell loop works everywhere.
var (
	keepAliveEntrypoint = "/bin/sh"
	keepAliveArgs       = []string{"-c", "while sleep 2147483647; do :; done"}
)

// CreateOptions carries host-environment choices that the Spec does not: how to
// map users and relabel SELinux volumes. They are computed once by the CLI
// (from flags + runtime probing) and threaded through.
type CreateOptions struct {
	// Userns is the value for --userns (e.g. "keep-id"); empty omits the flag.
	Userns string
	// SELinuxLabel is "z" (shared) or "Z" (private), appended to the default
	// workspace bind mount; empty omits relabeling.
	SELinuxLabel string
}

// BuildImageTag is the local tag devc assigns to an image it builds.
func BuildImageTag(spec *config.Spec) string { return "devc/" + spec.ID + ":latest" }

// BuildArgs constructs the `build` argv for a KindBuild spec.
func BuildArgs(spec *config.Spec, platform string) ([]string, error) {
	if spec.Image == nil || spec.Image.Build == nil {
		return nil, fmt.Errorf("BuildArgs called on a spec without build")
	}
	b := spec.Image.Build
	args := []string{"build", "--tag", BuildImageTag(spec)}
	if b.Dockerfile != "" {
		args = append(args, "--file", b.Dockerfile)
	}
	if b.Target != "" {
		args = append(args, "--target", b.Target)
	}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	for k, v := range sortedPairs(b.Args) {
		args = append(args, "--build-arg", k+"="+v)
	}
	for _, c := range b.CacheFrom {
		args = append(args, "--cache-from", c)
	}
	args = append(args, b.Options...)
	ctxDir := b.Context
	if ctxDir == "" {
		ctxDir = "."
	}
	args = append(args, ctxDir)
	return args, nil
}

// RunArgs constructs the `run -d` argv that creates the workspace container.
func RunArgs(spec *config.Spec, opts CreateOptions, platform string) ([]string, error) {
	if spec.Image == nil {
		return nil, fmt.Errorf("RunArgs called on a non-single-container spec")
	}
	img := spec.Image

	args := []string{"run", "--detach", "--name", ContainerName(spec)}
	args = append(args, labelArgs(Labels(spec))...)

	if platform != "" {
		args = append(args, "--platform", platform)
	}
	if opts.Userns != "" {
		args = append(args, "--userns", opts.Userns)
	}

	// Workspace bind mount: an explicit workspaceMount (already in --mount
	// syntax) wins; otherwise a default -v with optional SELinux relabel.
	if img.WorkspaceMount != "" {
		args = append(args, "--mount", img.WorkspaceMount)
	} else {
		v := spec.LocalWorkspaceFolder + ":" + spec.ContainerWorkspaceFolder
		if opts.SELinuxLabel != "" {
			v += ":" + opts.SELinuxLabel
		}
		args = append(args, "--volume", v)
	}
	for _, m := range img.Mounts {
		args = append(args, "--mount", m)
	}

	args = append(args, "--workdir", spec.ContainerWorkspaceFolder)

	for k, v := range sortedPairs(img.ContainerEnv) {
		args = append(args, "--env", k+"="+v)
	}
	if spec.ContainerUser != "" {
		args = append(args, "--user", spec.ContainerUser)
	}
	if img.Init {
		args = append(args, "--init")
	}
	if img.Privileged {
		args = append(args, "--privileged")
	}
	for _, c := range img.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, s := range img.SecurityOpt {
		args = append(args, "--security-opt", s)
	}
	for _, p := range img.AppPorts {
		args = append(args, "--publish", publishSpec(p))
	}
	args = append(args, img.RunArgs...)

	if spec.OverrideCommand {
		args = append(args, "--entrypoint", keepAliveEntrypoint)
	}

	imageRef := img.Image
	if spec.Kind == config.KindBuild {
		imageRef = BuildImageTag(spec)
	}
	args = append(args, imageRef)

	if spec.OverrideCommand {
		args = append(args, keepAliveArgs...)
	}
	return args, nil
}

// publishSpec renders a PortForward as a runtime --publish value.
func publishSpec(p config.PortForward) string {
	host := strconv.Itoa(p.HostPort)
	ctr := strconv.Itoa(p.ContainerPort)
	if p.HostIP != "" {
		return p.HostIP + ":" + host + ":" + ctr
	}
	return host + ":" + ctr
}

// Build runs the image build for a KindBuild spec.
func Build(ctx context.Context, r runtime.Runner, spec *config.Spec, platform string, io runtime.IO) error {
	args, err := BuildArgs(spec, platform)
	if err != nil {
		return err
	}
	return r.Run(ctx, args, io)
}

// Create runs the container (run -d) and returns its ID.
func Create(ctx context.Context, r runtime.Runner, spec *config.Spec, opts CreateOptions, platform string) (string, error) {
	args, err := RunArgs(spec, opts, platform)
	if err != nil {
		return "", err
	}
	out, err := r.Output(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Start starts an existing (stopped) container.
func Start(ctx context.Context, r runtime.Runner, name string) error {
	_, err := r.Output(ctx, "start", name)
	return err
}

// Stop stops a running container.
func Stop(ctx context.Context, r runtime.Runner, name string) error {
	_, err := r.Output(ctx, "stop", name)
	return err
}

// Remove force-removes a container (ignoring absence).
func Remove(ctx context.Context, r runtime.Runner, name string) error {
	_, err := r.Output(ctx, "rm", "--force", name)
	return err
}

// ExecArgs constructs an `exec` argv running argvIn as the given user in the
// working directory, injecting env. When interactive, a TTY is requested; the
// SSH transport must never set interactive.
func ExecArgs(name, user, workdir string, env map[string]string, interactive, tty bool, argvIn []string) []string {
	args := []string{"exec"}
	if interactive {
		args = append(args, "--interactive")
	}
	if tty {
		args = append(args, "--tty")
	}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	if user != "" {
		args = append(args, "--user", user)
	}
	for k, v := range sortedPairs(env) {
		args = append(args, "--env", k+"="+v)
	}
	args = append(args, name)
	args = append(args, argvIn...)
	return args
}
