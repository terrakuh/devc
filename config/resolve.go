package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Resolve turns a Loaded (post-substitution) config into a validated Spec. It
// detects the bring-up kind, applies defaults, parses ports, and rejects
// unsupported or contradictory configurations.
//
// Substitute must have been called on l first (Load + WorkspaceID + Substitute).
func Resolve(l *Loaded) (*Spec, error) {
	r := &l.Raw

	kind, err := detectKind(r)
	if err != nil {
		return nil, err
	}

	if len(r.Features) > 0 && string(r.Features) != "null" && string(r.Features) != "{}" {
		return nil, fmt.Errorf("devcontainer 'features' are not supported by devc")
	}
	if len(r.HostRequirements) > 0 && string(r.HostRequirements) != "null" {
		return nil, fmt.Errorf("devcontainer 'hostRequirements' are not supported by devc")
	}
	if r.UpdateRemoteUserUID != nil && *r.UpdateRemoteUserUID {
		return nil, fmt.Errorf("'updateRemoteUserUID' is not supported by devc (rootless podman uses --userns=keep-id instead)")
	}

	name := r.Name
	if name == "" {
		name = filepath.Base(l.LocalWorkspaceFolder)
	}
	id := WorkspaceID(name, l.LocalWorkspaceFolder)

	spec := &Spec{
		ID:                       id,
		Name:                     slug(name),
		Kind:                     kind,
		ConfigPath:               l.ConfigPath,
		LocalWorkspaceFolder:     l.LocalWorkspaceFolder,
		ContainerWorkspaceFolder: r.WorkspaceFolder,
		ContainerUser:            r.ContainerUser,
		RemoteUser:               r.RemoteUser,
		RemoteEnv:                nonEmptyMap(r.RemoteEnv),
		OverrideCommand:          defaultOverrideCommand(r, kind),
		Hooks: Hooks{
			Initialize:    r.InitializeCommand,
			OnCreate:      r.OnCreateCommand,
			UpdateContent: r.UpdateContentCommand,
			PostCreate:    r.PostCreateCommand,
			PostStart:     r.PostStartCommand,
			PostAttach:    r.PostAttachCommand,
		},
	}

	if spec.ShutdownAction, err = resolveShutdown(r, kind); err != nil {
		return nil, err
	}
	if spec.EnvProbe, err = resolveEnvProbe(r); err != nil {
		return nil, err
	}
	if spec.WaitFor, err = resolveWaitFor(r); err != nil {
		return nil, err
	}
	if spec.Ports, err = parsePorts(r.ForwardPorts); err != nil {
		return nil, fmt.Errorf("forwardPorts: %w", err)
	}

	switch kind {
	case KindImage, KindBuild:
		if spec.Image, err = resolveImage(r, kind, l); err != nil {
			return nil, err
		}
		if spec.ContainerWorkspaceFolder == "" {
			spec.ContainerWorkspaceFolder = "/workspaces/" + slug(filepath.Base(l.LocalWorkspaceFolder))
		}
	case KindCompose:
		if err := rejectSingleContainerKeys(r); err != nil {
			return nil, err
		}
		if spec.Compose, err = resolveCompose(r, l); err != nil {
			return nil, err
		}
		if spec.ContainerWorkspaceFolder == "" {
			return nil, fmt.Errorf("compose devcontainer requires 'workspaceFolder'")
		}
	}

	return spec, nil
}

// detectKind determines the bring-up strategy and rejects ambiguous configs.
func detectKind(r *Raw) (Kind, error) {
	hasImage := r.Image != ""
	hasBuild := r.Build != nil && (r.Build.Dockerfile != "")
	hasCompose := len(r.DockerComposeFile) > 0

	switch {
	case hasCompose && (hasImage || hasBuild):
		return "", fmt.Errorf("config sets both 'dockerComposeFile' and 'image'/'build'; choose one")
	case hasImage && hasBuild:
		return "", fmt.Errorf("config sets both 'image' and 'build'; choose one")
	case hasCompose:
		if r.Service == "" {
			return "", fmt.Errorf("compose devcontainer requires 'service'")
		}
		return KindCompose, nil
	case hasBuild:
		return KindBuild, nil
	case hasImage:
		return KindImage, nil
	default:
		return "", fmt.Errorf("config must set one of 'image', 'build', or 'dockerComposeFile'")
	}
}

// rejectSingleContainerKeys errors when a compose config also sets properties
// that only apply to single-container bring-up. devc never generates a compose
// override file, so silently ignoring these would be a footgun.
func rejectSingleContainerKeys(r *Raw) error {
	var offending []string
	if r.Image != "" {
		offending = append(offending, "image")
	}
	if r.Build != nil {
		offending = append(offending, "build")
	}
	if len(r.Mounts) > 0 {
		offending = append(offending, "mounts")
	}
	if r.WorkspaceMount != "" {
		offending = append(offending, "workspaceMount")
	}
	if len(r.ContainerEnv) > 0 {
		offending = append(offending, "containerEnv")
	}
	if r.ContainerUser != "" {
		offending = append(offending, "containerUser")
	}
	if len(r.RunArgs) > 0 {
		offending = append(offending, "runArgs")
	}
	if len(r.AppPort) > 0 {
		offending = append(offending, "appPort")
	}
	if r.Privileged != nil {
		offending = append(offending, "privileged")
	}
	if len(r.CapAdd) > 0 {
		offending = append(offending, "capAdd")
	}
	if len(r.SecurityOpt) > 0 {
		offending = append(offending, "securityOpt")
	}
	if len(offending) > 0 {
		return fmt.Errorf("these properties only apply to single-container devcontainers and cannot be used with 'dockerComposeFile' (devc does not generate a compose override): %s", strings.Join(offending, ", "))
	}
	return nil
}

func resolveImage(r *Raw, kind Kind, l *Loaded) (*ImageSpec, error) {
	img := &ImageSpec{
		Image:          r.Image,
		WorkspaceMount: expandMountTilde(r.WorkspaceMount),
		Mounts:         expandMountsTilde(r.Mounts),
		ContainerEnv:   nonEmptyMap(r.ContainerEnv),
		RunArgs:        r.RunArgs,
		Init:           boolValue(r.Init, false),
		Privileged:     boolValue(r.Privileged, false),
		CapAdd:         r.CapAdd,
		SecurityOpt:    r.SecurityOpt,
	}
	ap, err := parsePorts(r.AppPort)
	if err != nil {
		return nil, fmt.Errorf("appPort: %w", err)
	}
	img.AppPorts = ap

	if kind == KindBuild {
		configDir := filepath.Dir(l.ConfigPath)
		ctxDir := r.Build.Context
		if ctxDir == "" {
			ctxDir = "."
		}
		img.Build = &BuildSpec{
			Dockerfile: absFrom(configDir, r.Build.Dockerfile),
			Context:    absFrom(configDir, ctxDir),
			Args:       nonEmptyMap(r.Build.Args),
			Target:     r.Build.Target,
			CacheFrom:  r.Build.CacheFrom,
			Options:    r.Build.Options,
		}
	}
	return img, nil
}

func resolveCompose(r *Raw, l *Loaded) (*ComposeSpec, error) {
	configDir := filepath.Dir(l.ConfigPath)
	files := make([]string, 0, len(r.DockerComposeFile))
	for _, f := range r.DockerComposeFile {
		files = append(files, absFrom(configDir, f))
	}
	return &ComposeSpec{
		Files:       files,
		Service:     r.Service,
		RunServices: r.RunServices,
	}, nil
}

// expandMountsTilde expands a leading ~ in the source of every mount string.
func expandMountsTilde(mounts []string) []string {
	if len(mounts) == 0 {
		return mounts
	}
	out := make([]string, len(mounts))
	for i, m := range mounts {
		out[i] = expandMountTilde(m)
	}
	return out
}

// expandMountTilde expands a leading ~ or ~/ in the source/src field of a mount
// specification ("type=bind,source=~/project,target=/x"). Container runtimes do
// not expand ~ (only shells do), so without this a mount whose source starts
// with ~ silently binds a literal directory named "~". Only the source field is
// touched; other fields, mid-path ~, and the ~user form are left alone.
func expandMountTilde(mount string) string {
	if mount == "" || !strings.Contains(mount, "~") {
		return mount
	}
	parts := strings.Split(mount, ",")
	for i, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "source", "src":
			parts[i] = k + "=" + expandTilde(v)
		}
	}
	return strings.Join(parts, ",")
}

// expandTilde replaces a leading ~ (alone or before a /) with the user's home
// directory. It honors $HOME via os.UserHomeDir; if that cannot be resolved the
// path is returned unchanged.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return home + p[1:] // keep the separator: "~/x" -> "<home>/x"
}

func defaultOverrideCommand(r *Raw, kind Kind) bool {
	if r.OverrideCommand != nil {
		return *r.OverrideCommand
	}
	// Spec default: true for single-container, false for compose.
	return kind != KindCompose
}

func resolveShutdown(r *Raw, kind Kind) (ShutdownAction, error) {
	if r.ShutdownAction == "" {
		if kind == KindCompose {
			return ShutdownStopCompose, nil
		}
		return ShutdownStopCtr, nil
	}
	switch ShutdownAction(r.ShutdownAction) {
	case ShutdownNone, ShutdownStopCtr, ShutdownStopCompose:
		return ShutdownAction(r.ShutdownAction), nil
	default:
		return "", fmt.Errorf("invalid shutdownAction %q", r.ShutdownAction)
	}
}

func resolveEnvProbe(r *Raw) (EnvProbe, error) {
	if r.UserEnvProbe == "" {
		return EnvProbeLoginInteractive, nil
	}
	switch EnvProbe(r.UserEnvProbe) {
	case EnvProbeNone, EnvProbeLogin, EnvProbeInteractive, EnvProbeLoginInteractive:
		return EnvProbe(r.UserEnvProbe), nil
	default:
		return "", fmt.Errorf("invalid userEnvProbe %q", r.UserEnvProbe)
	}
}

func resolveWaitFor(r *Raw) (HookName, error) {
	if r.WaitFor == "" {
		return HookUpdateContent, nil
	}
	switch HookName(r.WaitFor) {
	case HookOnCreate, HookUpdateContent, HookPostCreate, HookPostStart, HookPostAttach:
		return HookName(r.WaitFor), nil
	default:
		return "", fmt.Errorf("invalid waitFor %q", r.WaitFor)
	}
}

// parsePorts converts the string port entries ("3000", "127.0.0.1:3000",
// "8080:80") into PortForward values.
func parsePorts(list PortList) ([]PortForward, error) {
	var out []PortForward
	for _, entry := range list {
		pf, err := parsePort(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, pf)
	}
	return out, nil
}

func parsePort(entry string) (PortForward, error) {
	parts := strings.Split(entry, ":")
	switch len(parts) {
	case 1: // "3000": same port on host and container
		p, err := atoiPort(parts[0])
		if err != nil {
			return PortForward{}, err
		}
		return PortForward{HostPort: p, ContainerPort: p}, nil
	case 2:
		// Two forms with a colon: "hostPort:containerPort" (both numeric) and
		// "IP:port" (a bind address). Accept both.
		if isNumeric(parts[0]) {
			hp, err := atoiPort(parts[0])
			if err != nil {
				return PortForward{}, err
			}
			cp, err := atoiPort(parts[1])
			if err != nil {
				return PortForward{}, err
			}
			return PortForward{HostPort: hp, ContainerPort: cp}, nil
		}
		cp, err := atoiPort(parts[1])
		if err != nil {
			return PortForward{}, err
		}
		return PortForward{HostIP: parts[0], HostPort: cp, ContainerPort: cp}, nil
	default:
		return PortForward{}, fmt.Errorf("invalid port %q", entry)
	}
}

func atoiPort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port number %q", s)
	}
	return n, nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// WorkspaceID is a stable identifier for a workspace: a slug of its name plus
// the first 8 hex of the sha256 of the absolute local folder. Stable across
// rebuilds, unique across two checkouts of the same repo, and safe in labels,
// container names, DNS names, and file paths.
func WorkspaceID(name, localFolder string) string {
	sum := sha256.Sum256([]byte(localFolder))
	return slug(name) + "-" + hex.EncodeToString(sum[:])[:8]
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases s and collapses every run of non-alphanumeric characters to a
// single hyphen, trimming leading/trailing hyphens. An empty result becomes
// "devc".
func slug(s string) string {
	s = strings.ToLower(s)
	s = slugInvalid.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "devc"
	}
	return s
}

func absFrom(base, p string) string {
	if p == "" {
		return ""
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func nonEmptyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
