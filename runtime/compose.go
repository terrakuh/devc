package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Compose is a resolved compose implementation: a CLIRunner that invokes it
// (e.g. Bin="podman", Base=["compose"], or Bin="docker-compose") plus a
// human-readable label for diagnostics.
type Compose struct {
	*CLIRunner
	Label string
}

// composeCandidate is one compose implementation to probe, in argv terms.
type composeCandidate struct {
	bin   string
	base  []string
	label string
}

// candidatesFor orders compose implementations to try, preferring the one that
// matches the selected runtime, then its alternates.
func candidatesFor(runtimeName string) []composeCandidate {
	podman := []composeCandidate{
		{"podman", []string{"compose"}, "podman compose"},
		{"podman-compose", nil, "podman-compose"},
	}
	docker := []composeCandidate{
		{"docker", []string{"compose"}, "docker compose"},
		{"docker-compose", nil, "docker-compose"},
	}
	if runtimeName == string(Docker) {
		return append(docker, podman...)
	}
	return append(podman, docker...)
}

// DetectCompose resolves a compose implementation. Selection order:
//
//	override (--compose-cmd flag) -> DEVC_COMPOSE env -> probe candidates
//
// An override is a full command string ("podman compose", "docker-compose")
// and is used as-is without probing. Otherwise each candidate is probed with
// `<cmd> version` and the first that succeeds wins.
func DetectCompose(ctx context.Context, runtimeName, override string) (*Compose, error) {
	if override == "" {
		override = os.Getenv("DEVC_COMPOSE")
	}
	if override != "" {
		fields := strings.Fields(override)
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty --compose-cmd")
		}
		bin, err := exec.LookPath(fields[0])
		if err != nil {
			return nil, fmt.Errorf("compose command %q not found on PATH: %w", fields[0], err)
		}
		return &Compose{
			CLIRunner: &CLIRunner{Bin: bin, Base: fields[1:]},
			Label:     override,
		}, nil
	}

	var tried []string
	for _, c := range candidatesFor(runtimeName) {
		bin, err := exec.LookPath(c.bin)
		if err != nil {
			continue
		}
		runner := &Compose{CLIRunner: &CLIRunner{Bin: bin, Base: c.base}, Label: c.label}
		if probeCompose(ctx, runner) {
			return runner, nil
		}
		tried = append(tried, c.label)
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("no compose implementation found (looked for podman compose, podman-compose, docker compose, docker-compose); set --compose-cmd or DEVC_COMPOSE")
	}
	return nil, fmt.Errorf("found compose commands but none responded to `version`: %s", strings.Join(tried, ", "))
}

// probeCompose reports whether `<compose> version` succeeds.
func probeCompose(ctx context.Context, c *Compose) bool {
	_, err := c.Output(ctx, "version")
	return err == nil
}
