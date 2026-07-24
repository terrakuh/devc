package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"

	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/keys"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/sshconf"
	"github.com/terrakuh/devc/state"
)

// provision installs the SSH agent and credentials into the running container
// and (re)writes the host-side ssh config so `ssh devc.<name>` and VSCodium can
// connect. containerRef is the container the agent runs in (name or id).
//
// It is idempotent and runs on every `devc up`.
func provision(ctx context.Context, e *env, containerRef string) error {
	dir, err := state.For(e.spec.ID)
	if err != nil {
		return err
	}
	alias := sshAlias(e.spec)

	keySet, err := keys.Ensure(dir.Root, alias)
	if err != nil {
		return fmt.Errorf("prepare ssh keys: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate devc binary: %w", err)
	}
	if err := container.Inject(ctx, e.runner, container.InjectOptions{
		Container:         containerRef,
		AgentSource:       self,
		HostArch:          stdruntime.GOARCH,
		Version:           version,
		HostKeyFile:       keySet.HostPrivate,
		AuthorizedKeyFile: keySet.ClientPublic,
		Env:               e.effectiveEnv(),
	}); err != nil {
		return fmt.Errorf("inject agent: %w", err)
	}

	if err := writeSSHConfig(e, keySet, dir, self); err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}

	// Git config sync is a convenience, not part of the connection path: a
	// failure (no host git, unwritable HOME) warns but never fails `up`.
	if e.spec.Credentials.SyncGitConfig {
		if err := syncGitConfig(ctx, e, containerRef); err != nil && !e.flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: sync git config: %v\n", err)
		}
	}
	return nil
}

// writeSSHConfig renders the workspace Host block, writes it to the devc ssh
// config file, and ensures ~/.ssh/config includes it.
func writeSSHConfig(e *env, keySet *keys.Set, dir *state.Dir, devcBin string) error {
	var forwards []sshconf.Forward
	for _, p := range e.spec.Ports {
		forwards = append(forwards, sshconf.Forward{HostPort: p.HostPort, ContainerPort: p.ContainerPort})
	}
	ctlDir, err := ensureControlDir(e.spec.ID)
	if err != nil {
		return err
	}
	block := sshconf.Render(sshconf.Params{
		Alias:          sshAlias(e.spec),
		User:           e.spec.RemoteUser,
		DevcBin:        devcBin,
		ConfigPath:     e.spec.ConfigPath,
		RuntimePath:    runtimeBinPath(e.runner),
		WorkspaceName:  e.spec.Name,
		IdentityFile:   keySet.ClientPrivate,
		KnownHostsFile: keySet.KnownHosts,
		ControlDir:     ctlDir,
		Forwards:       forwards,
		ForwardAgent:   e.spec.Credentials.ForwardAgent,
	})

	confPath := devcSSHConfigPath()
	if err := sshconf.WriteWorkspaceConfig(confPath, sshconf.File(block)); err != nil {
		return err
	}
	if e.flags.quiet {
		return nil
	}
	changed, err := sshconf.EnsureInclude(userSSHConfigPath(), confPath)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stderr, "devc: added `Include %s` to %s\n", confPath, userSSHConfigPath())
	}
	return nil
}

// runtimeBinPath returns the absolute path of the container runtime binary for
// baking into the ssh ProxyCommand, or "" if the runner cannot report it (then
// the ProxyCommand falls back to a PATH lookup at connect time).
func runtimeBinPath(r runtime.Runner) string {
	if bp, ok := r.(interface{ BinPath() string }); ok {
		return bp.BinPath()
	}
	return ""
}

// ensureControlDir returns (and creates, 0700) a short directory for the SSH
// ControlMaster socket. It lives under /tmp, not the workspace state dir,
// because the state dir sits deep under $HOME and the resulting socket path can
// exceed the ~104-char sun_path limit - which makes ControlMaster silently fail
// and breaks editors that rely on connection multiplexing. The per-uid prefix
// keeps sockets isolated on a shared /tmp; the workspace id keeps them unique.
func ensureControlDir(id string) (string, error) {
	dir := filepath.Join("/tmp", fmt.Sprintf("devc-%d", os.Getuid()), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create control socket dir: %w", err)
	}
	return dir, nil
}

// removeControlDir deletes a workspace's control-socket directory (down --purge).
func removeControlDir(id string) error {
	dir := filepath.Join("/tmp", fmt.Sprintf("devc-%d", os.Getuid()), id)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// devcSSHConfigPath is ~/.config/devc/ssh.config (or under XDG_CONFIG_HOME).
func devcSSHConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "devc", "ssh.config")
}

func userSSHConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}
