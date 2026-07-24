package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// gitConfigSyncKeys is the allowlist of host git settings copied into the
// container: identity and commit-signing. It is kept small and free of paths
// (editor, credential helper, key files) that would not resolve in the
// container. All keys are two-part, so renderGitConfig needs no subsections.
var gitConfigSyncKeys = []string{
	"user.name",
	"user.email",
	"user.signingkey",
	"commit.gpgsign",
	"tag.gpgsign",
	"gpg.format",
	"init.defaultbranch",
}

// syncGitConfig copies the allowlisted host git config into the container user's
// XDG git config (~/.config/git/config). Git reads that below ~/.gitconfig, so
// it acts as defaults the container can still override and never clobbers a
// user-written ~/.gitconfig.
//
// The caller treats any error as a warning, not a failure of `devc up`.
func syncGitConfig(ctx context.Context, e *env, containerRef string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git not found on host: %w", err)
	}

	content := renderGitConfig(gatherHostGitConfig(ctx, git))
	if content == "" {
		return nil // nothing to sync
	}

	// Write as the remote user so HOME and ownership are right. The shell uses
	// XDG_CONFIG_HOME when set, else ~/.config.
	const script = `set -e; dir="${XDG_CONFIG_HOME:-$HOME/.config}/git"; mkdir -p "$dir"; cat > "$dir/config"`
	argv := container.ExecArgs(containerRef, e.spec.RemoteUser, e.spec.ContainerWorkspaceFolder,
		e.effectiveEnv(), true, false, []string{"sh", "-c", script})
	io := runtime.IO{Stdin: strings.NewReader(content), Stdout: os.Stderr, Stderr: os.Stderr}
	return e.runner.Run(ctx, argv, io)
}

// gatherHostGitConfig reads each allowlisted key from the host git config in
// allowlist order, skipping unset keys. It runs from the home dir so a repo's
// local config does not leak in, while global, XDG, and system values still do.
func gatherHostGitConfig(ctx context.Context, git string) [][2]string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	var out [][2]string
	for _, key := range gitConfigSyncKeys {
		cmd := exec.CommandContext(ctx, git, "config", "--get", key) //nolint:gosec // key is from a fixed allowlist
		cmd.Dir = home
		b, err := cmd.Output()
		if err != nil {
			continue // unset (exit 1) or unreadable: skip
		}
		if v := strings.TrimRight(string(b), "\r\n"); v != "" {
			out = append(out, [2]string{key, v})
		}
	}
	return out
}

// renderGitConfig writes section.key pairs as git-config INI, grouping by
// section in first-seen order. Returns "" when there is nothing to write. Only
// two-part keys are supported, so there are no subsections to quote.
func renderGitConfig(pairs [][2]string) string {
	if len(pairs) == 0 {
		return ""
	}
	var b bytes.Buffer
	b.WriteString("# Managed by devc (credentials.syncGitConfig). Overwritten on `devc up`.\n")

	var order []string
	bySection := map[string][][2]string{}
	for _, kv := range pairs {
		section, name, ok := strings.Cut(kv[0], ".")
		if !ok {
			continue
		}
		if _, seen := bySection[section]; !seen {
			order = append(order, section)
		}
		bySection[section] = append(bySection[section], [2]string{name, kv[1]})
	}
	for _, section := range order {
		fmt.Fprintf(&b, "[%s]\n", section)
		for _, kv := range bySection[section] {
			fmt.Fprintf(&b, "\t%s = %s\n", kv[0], kv[1])
		}
	}
	return b.String()
}
