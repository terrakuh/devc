package container

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/terrakuh/devc/runtime"
)

// AgentDir is where devc places the agent binary and its credentials inside the
// container.
const AgentDir = "/.devc"

// Agent file paths inside the container.
const (
	AgentBinary  = AgentDir + "/agent"
	AgentHostKey = AgentDir + "/host_key"
	AgentAuthKey = AgentDir + "/authorized_key"
	AgentEnvFile = AgentDir + "/env"
)

// InjectOptions parameterises agent injection.
type InjectOptions struct {
	// Container is the target container reference (name or id).
	Container string
	// AgentSource is the host path of the agent binary to copy, normally the
	// running devc binary itself (/proc/self/exe). It must be a static,
	// CGO-free linux binary of the container's architecture.
	AgentSource string
	// HostArch is the GOARCH of AgentSource; injection errors if it does not
	// match the container's architecture.
	HostArch string
	// Version is the expected agent version; a container whose agent already
	// reports it skips the binary copy.
	Version string
	// HostKeyFile and AuthorizedKeyFile are host paths to the agent's host key
	// and the single authorized client public key.
	HostKeyFile       string
	AuthorizedKeyFile string
	// Env is injected into every ssh session (remoteEnv plus the env probe).
	Env map[string]string
}

// Inject installs the agent binary and credentials into the container, idempotently.
// It skips the (large) binary copy when the container already runs the expected
// version. Keys and env are always refreshed (cheap, and keeps rotation simple).
//
// All in-container steps run as root (--user 0): /.devc lives at the filesystem
// root and the agent must be able to drop privileges to the session user, so
// setup cannot depend on the container's default exec user (which, e.g. under
// --userns=keep-id, is unprivileged).
func Inject(ctx context.Context, r runtime.Runner, opts InjectOptions) error {
	if err := ensureArch(ctx, r, opts.Container, opts.HostArch); err != nil {
		return err
	}
	if _, err := r.Output(ctx, rootExec(opts.Container, "mkdir", "-p", AgentDir)...); err != nil {
		return fmt.Errorf("create %s: %w", AgentDir, err)
	}
	if _, err := r.Output(ctx, rootExec(opts.Container, "chmod", "0700", AgentDir)...); err != nil {
		return err
	}

	if !agentUpToDate(ctx, r, opts.Container, opts.Version) {
		if err := cpInto(ctx, r, opts.AgentSource, opts.Container, AgentBinary); err != nil {
			return fmt.Errorf("copy agent binary: %w", err)
		}
		if _, err := r.Output(ctx, rootExec(opts.Container, "chmod", "0755", AgentBinary)...); err != nil {
			return err
		}
	}

	if err := cpInto(ctx, r, opts.HostKeyFile, opts.Container, AgentHostKey); err != nil {
		return fmt.Errorf("copy host key: %w", err)
	}
	if _, err := r.Output(ctx, rootExec(opts.Container, "chmod", "0600", AgentHostKey)...); err != nil {
		return err
	}
	if err := cpInto(ctx, r, opts.AuthorizedKeyFile, opts.Container, AgentAuthKey); err != nil {
		return fmt.Errorf("copy authorized key: %w", err)
	}
	if err := writeEnvFile(ctx, r, opts.Container, opts.Env); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	return nil
}

// rootExec builds an `exec --user 0 <container> <cmd...>` argv.
func rootExec(containerRef string, cmd ...string) []string {
	args := []string{"exec", "--user", "0", containerRef}
	return append(args, cmd...)
}

// ensureArch verifies the container's architecture matches the agent binary's.
func ensureArch(ctx context.Context, r runtime.Runner, containerRef, hostArch string) error {
	out, err := r.Output(ctx, "exec", containerRef, "uname", "-m")
	if err != nil {
		return fmt.Errorf("detect container architecture: %w", err)
	}
	ctrArch := normalizeArch(strings.TrimSpace(string(out)))
	if hostArch != "" && ctrArch != "" && ctrArch != hostArch {
		return fmt.Errorf("container architecture %q does not match the devc binary (%q); cross-architecture agent injection is not supported yet; run devc on a %s host or build a matching binary", ctrArch, hostArch, ctrArch)
	}
	return nil
}

// normalizeArch maps uname -m output to Go's GOARCH vocabulary.
func normalizeArch(m string) string {
	switch m {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv6l":
		return "arm"
	default:
		return m
	}
}

// agentUpToDate reports whether the installed agent already reports version.
func agentUpToDate(ctx context.Context, r runtime.Runner, containerRef, version string) bool {
	if version == "" {
		return false
	}
	out, err := r.Output(ctx, "exec", containerRef, AgentBinary, "version")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == version
}

// cpInto copies a host file to dest inside the container via `<runtime> cp`.
func cpInto(ctx context.Context, r runtime.Runner, src, containerRef, dest string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source %s: %w", src, err)
	}
	_, err := r.Output(ctx, "cp", src, containerRef+":"+dest)
	return err
}

// writeEnvFile writes the KEY=VALUE env file into the container by piping the
// content to `sh -c 'cat > file'`, avoiding a host temp file.
func writeEnvFile(ctx context.Context, r runtime.Runner, containerRef string, env map[string]string) error {
	var b strings.Builder
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// One KEY=VALUE per line; values with newlines are not supported (env
		// vars rarely contain them and the ssh env channel can't either).
		fmt.Fprintf(&b, "%s=%s\n", k, strings.ReplaceAll(env[k], "\n", " "))
	}
	io := runtime.IO{Stdin: strings.NewReader(b.String())}
	return r.Run(ctx, []string{"exec", "--interactive", "--user", "0", containerRef, "sh", "-c", "cat > " + AgentEnvFile}, io)
}

// AgentServeArgs builds the argv that runs the injected agent as an SSH server
// over the exec pipe: `exec -i --user 0 <ctr> /.devc/agent __serve`. The agent
// runs as root and drops to the session user itself (--user below), the same
// privilege model as sshd.
func AgentServeArgs(containerRef, user, cwd string, forwardAgent bool) []string {
	args := []string{"exec", "--interactive", "--user", "0", containerRef, AgentBinary, "__serve",
		"--host-key", AgentHostKey,
		"--authorized", AgentAuthKey,
		"--env-file", AgentEnvFile,
	}
	if user != "" {
		args = append(args, "--user", user)
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if forwardAgent {
		args = append(args, "--forward-agent")
	}
	return args
}
