package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
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

// AgentDirMode keeps AgentDir traversable (the trailing 1): the SFTP subsystem
// re-execs AgentBinary as the session user, who cannot exec a file it cannot
// reach. The files inside carry their own modes.
const AgentDirMode = "0711"

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
// Every runtime call costs tens to hundreds of milliseconds (an exec more than
// most), so the work is batched into at most three: one exec that prepares
// AgentDir and reports the architecture and installed agent version, a cp of
// the binary when it is outdated, and one exec that writes the secrets from
// stdin (never argv, which `ps` would show).
//
// All in-container steps run as root (--user 0): /.devc lives at the filesystem
// root and the agent must be able to drop privileges to the session user, so
// setup cannot depend on the container's default exec user (which, e.g. under
// --userns=keep-id, is unprivileged).
func Inject(ctx context.Context, r runtime.Runner, opts InjectOptions) error {
	out, err := r.Output(ctx, rootExec(opts.Container, "sh", "-c", prepareScript, "sh", AgentDir, AgentDirMode)...)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", AgentDir, err)
	}
	arch, installed, _ := strings.Cut(string(out), "\n")
	if err := checkArch(strings.TrimSpace(arch), opts.HostArch); err != nil {
		return err
	}

	if opts.Version == "" || strings.TrimSpace(installed) != opts.Version {
		if err := cpInto(ctx, r, opts.AgentSource, opts.Container, AgentBinary); err != nil {
			return fmt.Errorf("copy agent binary: %w", err)
		}
	}

	if err := writeSecrets(ctx, r, opts); err != nil {
		return fmt.Errorf("write agent credentials: %w", err)
	}
	return nil
}

// prepareScript creates AgentDir ($1) with mode $2, then prints `uname -m` and
// the installed agent's version (an empty line when there is none yet, or it
// cannot run - e.g. built for another architecture).
const prepareScript = `set -e
uname -m
mkdir -p "$1"
chmod "$2" "$1"
"$1/agent" version 2>/dev/null || echo`

// secretsScript installs the files streamed on stdin into AgentDir ($1): the
// host key, the authorized key and the env file, whose byte sizes are $2, $3
// and $4. dd reads one byte at a time because it shares the pipe: head -c or a
// block read may consume bytes that belong to the next file. Each is written to
// a temp file under umask 077 and renamed into place, so it is root-owned 0600
// even if an older copy had other owners or modes. The binary is (re)made
// executable in the same exec.
const secretsScript = `set -e
umask 077
cd "$1"
chmod 0755 agent
for f in host_key:$2 authorized_key:$3 env:$4; do
	name=${f%%:*}
	dd bs=1 count="${f#*:}" of="$name.tmp" 2>/dev/null
	chmod 0600 "$name.tmp"
	mv -f "$name.tmp" "$name"
done`

// rootExec builds an `exec --user 0 <container> <cmd...>` argv.
func rootExec(containerRef string, cmd ...string) []string {
	args := []string{"exec", "--user", "0", containerRef}
	return append(args, cmd...)
}

// checkArch verifies the container's architecture (uname -m) matches the agent binary's.
func checkArch(uname, hostArch string) error {
	ctrArch := normalizeArch(uname)
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

// cpInto copies a host file to dest inside the container via `<runtime> cp`,
// which keeps the file's mode.
func cpInto(ctx context.Context, r runtime.Runner, src, containerRef, dest string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source %s: %w", src, err)
	}
	_, err := r.Output(ctx, "cp", src, containerRef+":"+dest)
	return err
}

// writeSecrets streams the host key, the authorized key and the env file into
// the container in a single exec (see secretsScript).
func writeSecrets(ctx context.Context, r runtime.Runner, opts InjectOptions) error {
	hostKey, err := os.ReadFile(opts.HostKeyFile)
	if err != nil {
		return err
	}
	authKey, err := os.ReadFile(opts.AuthorizedKeyFile)
	if err != nil {
		return err
	}
	env := envFile(opts.Env)

	var stdin bytes.Buffer
	stdin.Write(hostKey)
	stdin.Write(authKey)
	stdin.WriteString(env)
	var stderr bytes.Buffer
	argv := []string{"exec", "--interactive", "--user", "0", opts.Container, "sh", "-c", secretsScript, "sh", AgentDir,
		strconv.Itoa(len(hostKey)), strconv.Itoa(len(authKey)), strconv.Itoa(len(env))}
	if err := r.Run(ctx, argv, runtime.IO{Stdin: &stdin, Stderr: &stderr}); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// envFile renders env as the agent's KEY=VALUE env file, sorted by key.
func envFile(env map[string]string) string {
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
	return b.String()
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
