package container

import (
	"context"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/runtime"
)

// ProbeEnv runs a shell inside the container to capture the environment a login
// and/or interactive shell would set (PATH additions from /etc/profile.d, nvm,
// etc.), per userEnvProbe. Without it, `ssh workspace 'go version'` would miss
// everything the profile scripts export, a classic and confusing failure.
//
// It returns nil for EnvProbeNone or on any error (the probe is best-effort; a
// failed probe must never block bring-up).
func ProbeEnv(ctx context.Context, r runtime.Runner, containerRef, user string, mode config.EnvProbe) map[string]string {
	flags := probeShellFlags(mode)
	if flags == "" {
		return nil
	}
	// `env` output, one KEY=VALUE per line. We accept the rare limitation that
	// values containing newlines are not captured faithfully (the ssh env
	// channel and our env file share the same constraint).
	argv := ExecArgs(containerRef, user, "", nil, false, false, []string{"/bin/sh", flags, "-c", "env"})
	out, err := r.Output(ctx, argv...)
	if err != nil {
		return nil
	}
	return parseEnvOutput(string(out))
}

// probeShellFlags maps an EnvProbe mode to the `sh` flag string.
func probeShellFlags(mode config.EnvProbe) string {
	switch mode {
	case config.EnvProbeLogin:
		return "-l"
	case config.EnvProbeInteractive:
		return "-i"
	case config.EnvProbeLoginInteractive:
		return "-li"
	default: // EnvProbeNone or unknown
		return ""
	}
}

// parseEnvOutput parses `env` output into a map, skipping malformed lines and a
// few variables that are process-specific and must not be pinned into sessions.
func parseEnvOutput(out string) map[string]string {
	skip := map[string]bool{"_": true, "SHLVL": true, "PWD": true, "OLDPWD": true}
	env := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || skip[k] {
			continue
		}
		env[k] = v
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
