// Package sshconf generates the OpenSSH client configuration that points an
// editor (VSCodium Remote-SSH) or the ssh CLI at a devc workspace. The Host
// block uses a ProxyCommand that runs `devc ssh --stdio`, so no port is
// published and no TCP listener exists; forwardPorts become LocalForward lines.
package sshconf

import (
	"fmt"
	"sort"
	"strings"
)

// Params are the inputs to a generated Host block.
type Params struct {
	// Alias is the ssh Host name, e.g. "devc.shop".
	Alias string
	// User is the login user reported to ssh (informational; the agent decides
	// the real session user). Defaults to "root".
	User string
	// DevcBin is the absolute path to the devc binary used in ProxyCommand.
	DevcBin string
	// ConfigPath is the absolute path to the workspace's devcontainer.json. It is
	// baked into the ProxyCommand as `--config` so resolution never depends on the
	// caller's working directory - VSCodium spawns the ProxyCommand from its own
	// cwd, where a bare `devc ssh` would find no devcontainer.json and exit.
	ConfigPath string
	// RuntimePath is the absolute path to the container runtime (podman/docker).
	// Baked into the ProxyCommand as `--runtime` so the transport does not depend
	// on PATH - GUI-launched editors spawn the ProxyCommand with a stripped
	// environment where a PATH lookup for podman/docker can fail. Empty omits it.
	RuntimePath string
	// WorkspaceName is passed to `devc ssh --stdio <name>` (the slug).
	WorkspaceName string
	// IdentityFile, KnownHostsFile point at the workspace's key material.
	IdentityFile   string
	KnownHostsFile string
	// ControlDir holds the multiplexing socket (ControlPath). Usually the
	// workspace state dir.
	ControlDir string
	// Forwards are host->container port forwards rendered as LocalForward.
	Forwards []Forward
}

// Forward is one LocalForward entry.
type Forward struct {
	HostPort      int
	ContainerPort int
}

// Render returns the Host block for a workspace. It is deterministic so the
// generated file is stable across `devc up` runs (no spurious diffs).
func Render(p Params) string {
	user := p.User
	if user == "" {
		user = "root"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n", p.Alias)
	fmt.Fprintf(&b, "    HostName              %s\n", p.Alias)
	fmt.Fprintf(&b, "    User                  %s\n", user)
	fmt.Fprintf(&b, "    ProxyCommand          %s\n", proxyCommand(p))
	fmt.Fprintf(&b, "    IdentityFile          %s\n", p.IdentityFile)
	fmt.Fprintf(&b, "    IdentitiesOnly        yes\n")
	fmt.Fprintf(&b, "    UserKnownHostsFile    %s\n", p.KnownHostsFile)
	fmt.Fprintf(&b, "    StrictHostKeyChecking yes\n")
	fmt.Fprintf(&b, "    ControlMaster         auto\n")
	fmt.Fprintf(&b, "    ControlPath           %s/control-%%r\n", p.ControlDir)
	fmt.Fprintf(&b, "    ControlPersist        10m\n")
	fmt.Fprintf(&b, "    ServerAliveInterval   30\n")
	fmt.Fprintf(&b, "    ForwardAgent          no\n")

	// Deterministic forward order.
	fwds := append([]Forward(nil), p.Forwards...)
	sort.Slice(fwds, func(i, j int) bool { return fwds[i].HostPort < fwds[j].HostPort })
	for _, f := range fwds {
		fmt.Fprintf(&b, "    LocalForward          %d localhost:%d\n", f.HostPort, f.ContainerPort)
	}
	return b.String()
}

// proxyCommand builds the
// `devc ssh --stdio --start [--config <path>] [--runtime <path>] <name>` line.
// The absolute --config keeps resolution independent of the caller's cwd, and
// the absolute --runtime keeps it independent of PATH - both matter because a
// GUI editor (VSCodium/open-remote-ssh) spawns the ProxyCommand from its own
// working directory with a stripped environment.
func proxyCommand(p Params) string {
	parts := []string{maybeQuote(p.DevcBin), "ssh", "--stdio", "--start"}
	if p.ConfigPath != "" {
		parts = append(parts, "--config", maybeQuote(p.ConfigPath))
	}
	if p.RuntimePath != "" {
		parts = append(parts, "--runtime", maybeQuote(p.RuntimePath))
	}
	parts = append(parts, maybeQuote(p.WorkspaceName))
	return strings.Join(parts, " ")
}

// maybeQuote single-quotes s only when it contains characters that a shell (or a
// ProxyCommand arg splitter) would treat specially. Plain paths - the common
// case - are left bare so a naive splitter that does not understand quoting
// (some editor SSH clients) still parses the arguments correctly.
func maybeQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, needsQuote) < 0 {
		return s
	}
	// Escape any embedded single quote as '\'' and wrap the whole thing.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// needsQuote reports whether r forces quoting: anything outside the safe set of
// path/identifier characters (letters, digits, and - _ . / @ : , =).
func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case strings.ContainsRune("-_./@:,=", r):
		return false
	default:
		return true
	}
}

// File wraps one or more Host blocks with a managed-file header.
func File(blocks ...string) string {
	var b strings.Builder
	b.WriteString("# Managed by devc. Do not edit; regenerated on `devc up`.\n\n")
	b.WriteString(strings.Join(blocks, "\n"))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
