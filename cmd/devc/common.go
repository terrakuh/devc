package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// commonFlags holds the flags shared by the container-touching commands.
type commonFlags struct {
	path       string
	name       string
	configFile string
	runtime    string
	composeCmd string
	platform   string
	userns     string
	selinux    string
	quiet      bool

	// Overrides for customizations.devc.credentials. Unset means "use the config".
	forwardAgent  optionalBool
	syncGitConfig optionalBool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.path, "path", ".", "project directory or path to a devcontainer.json")
	fs.StringVar(&c.name, "name", "", "target a workspace by its `devc list` name instead of --path")
	fs.StringVar(&c.name, "n", "", "shorthand for --name")
	fs.StringVar(&c.configFile, "config", "", "explicit devcontainer.json to use")
	fs.StringVar(&c.runtime, "runtime", "", "container runtime: podman or docker (default: autodetect)")
	fs.StringVar(&c.composeCmd, "compose-cmd", "", `compose command, e.g. "podman compose" (default: autodetect)`)
	fs.StringVar(&c.platform, "platform", "", "target platform for build/run, e.g. linux/amd64")
	fs.StringVar(&c.userns, "userns", "", "value for --userns (default: keep-id under rootless podman)")
	fs.StringVar(&c.selinux, "selinux", "auto", "SELinux relabel of the workspace mount: auto|z|Z|none")
	fs.BoolVar(&c.quiet, "q", false, "suppress warnings")
	fs.Var(&c.forwardAgent, "forward-agent", "override credentials.forwardAgent (ssh-agent forwarding)")
	fs.Var(&c.syncGitConfig, "sync-git-config", "override credentials.syncGitConfig")
}

// optionalBool is a bool flag that remembers whether it was set, so a CLI value
// can override the config while an absent flag leaves the config untouched.
type optionalBool struct {
	set bool
	val bool
}

func (o *optionalBool) String() string {
	if o == nil || !o.set {
		return ""
	}
	return strconv.FormatBool(o.val)
}

func (o *optionalBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	o.val, o.set = v, true
	return nil
}

// IsBoolFlag lets the flag be given as "--forward-agent" (no value), like -q.
func (o *optionalBool) IsBoolFlag() bool { return true }

// env bundles everything a command needs after the common setup: the resolved
// spec, a runner, and the create options derived from flags + runtime probing.
type env struct {
	spec    *config.Spec
	runner  runtime.Runner
	create  container.CreateOptions
	flags   *commonFlags
	warns   []string
	compose *runtime.Compose // lazily detected; use composeImpl()

	// sessionEnv is the environment injected into ssh sessions and container
	// hooks: the userEnvProbe result overlaid by remoteEnv. Populated during
	// `up` once the container is running; nil elsewhere (falls back to remoteEnv).
	sessionEnv map[string]string
}

// effectiveEnv is the environment for in-container work: the resolved session
// env when available, otherwise the config's remoteEnv.
func (e *env) effectiveEnv() map[string]string {
	if e.sessionEnv != nil {
		return e.sessionEnv
	}
	return e.spec.RemoteEnv
}

// setup resolves the spec, selects the runtime, and computes create options.
func setup(ctx context.Context, c *commonFlags) (*env, error) {
	runner, err := runtime.Detect(c.runtime)
	if err != nil {
		return nil, err
	}

	// -n/--name selects a workspace by its `devc list` name; resolve it to the
	// local folder (via container labels) and load config from there.
	path := c.path
	if c.name != "" {
		if c.configFile != "" {
			return nil, fmt.Errorf("--config cannot be combined with -n/--name")
		}
		if path, err = workspaceFolderByName(ctx, runner, c.name); err != nil {
			return nil, err
		}
	}

	spec, warns, err := config.LoadSpec(path, c.configFile)
	if err != nil {
		return nil, err
	}
	if !c.quiet {
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	if c.forwardAgent.set {
		spec.Credentials.ForwardAgent = c.forwardAgent.val
	}
	if c.syncGitConfig.set {
		spec.Credentials.SyncGitConfig = c.syncGitConfig.val
	}

	opts := container.CreateOptions{
		Userns:       c.userns, // empty unless the user asks; see resolveUserns note
		SELinuxLabel: resolveSELinux(c.selinux),
	}
	return &env{spec: spec, runner: runner, create: opts, flags: c, warns: warns}, nil
}

// workspaceFolderByName resolves a `devc list` name (or id) to its local
// workspace folder using container labels, so a command can target a workspace
// from any directory.
func workspaceFolderByName(ctx context.Context, r runtime.Runner, name string) (string, error) {
	infos, err := container.List(ctx, r)
	if err != nil {
		return "", err
	}
	return selectWorkspaceFolder(infos, name)
}

// selectWorkspaceFolder picks the local folder whose container matches name by
// devc name or id label. It errors when nothing matches or when the name maps to
// more than one folder (two checkouts sharing a name).
func selectWorkspaceFolder(infos []*container.Info, name string) (string, error) {
	folders := map[string]bool{}
	for _, info := range infos {
		labels := info.Config.Labels
		if labels[container.LabelName] == name || labels[container.LabelID] == name {
			if f := labels[container.LabelLocal]; f != "" {
				folders[f] = true
			}
		}
	}
	switch len(folders) {
	case 0:
		return "", fmt.Errorf("no workspace named %q (see `devc list`)", name)
	case 1:
		for f := range folders {
			return f, nil
		}
	}
	list := make([]string, 0, len(folders))
	for f := range folders {
		list = append(list, f)
	}
	sort.Strings(list)
	return "", fmt.Errorf("workspace name %q is ambiguous across %s; use --path", name, strings.Join(list, ", "))
}

// resolveUserns note: devc does NOT default to --userns=keep-id. Under plain
// rootless podman, container UID 0 already maps to the invoking host user, so
// root-in-container writes land as the caller's files (the ownership keep-id
// was meant to provide) while leaving root available for /.devc setup and the
// agent's privilege drop. keep-id, by contrast, makes the default exec user
// non-root (breaking `mkdir /.devc`) and is only useful for a fixed non-root
// container user, so it stays opt-in via --userns.

// resolveSELinux maps the --selinux flag to a mount label option. "auto" uses
// shared relabeling (:z) when the host has SELinux enabled.
func resolveSELinux(flagVal string) string {
	switch flagVal {
	case "z", "Z":
		return flagVal
	case "none", "":
		return ""
	case "auto":
		if selinuxEnabled() {
			return "z"
		}
		return ""
	default:
		return ""
	}
}

// selinuxEnabled reports whether the host enforces SELinux, best-effort via the
// selinuxenabled(8) exit code.
func selinuxEnabled() bool {
	bin, err := exec.LookPath("selinuxenabled")
	if err != nil {
		return false
	}
	return exec.Command(bin).Run() == nil //nolint:gosec // fixed binary, no args
}

// composeImpl lazily detects and caches the compose implementation. Only called
// on the compose path.
func (e *env) composeImpl(ctx context.Context) (*runtime.Compose, error) {
	if e.compose == nil {
		c, err := runtime.DetectCompose(ctx, e.runner.Name(), e.flags.composeCmd)
		if err != nil {
			return nil, err
		}
		e.compose = c
	}
	return e.compose, nil
}

// attachRef resolves the container devc attaches to for exec/status/ssh,
// independent of bring-up kind: the named container for single-container
// workspaces, or the compose service's container. It returns a runtime
// reference (id or name) and the container Info, with info == nil when no
// container exists yet.
func (e *env) attachRef(ctx context.Context) (ref string, info *container.Info, err error) {
	if e.spec.Kind == config.KindCompose {
		project := container.ProjectName(e.spec)
		info, err = container.FindComposeService(ctx, e.runner, project, e.spec.Compose.Service)
		if err != nil || info == nil {
			return "", info, err
		}
		return info.ID, info, nil
	}
	name := container.ContainerName(e.spec)
	info, err = container.Find(ctx, e.runner, name)
	return name, info, err
}
