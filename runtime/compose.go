package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// `<cmd> version` and the first that succeeds wins. Probing is slow (`podman
// compose` hands off to a separate provider, often Python), so the winner is
// cached and reused until the installed compose binaries change; see
// composeFingerprint.
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

	cands := candidatesFor(runtimeName)
	fingerprint := composeFingerprint(cands)
	if c := loadComposeCache(runtimeName, fingerprint); c != nil {
		return c, nil
	}
	var tried []string
	for _, c := range cands {
		bin, err := exec.LookPath(c.bin)
		if err != nil {
			continue
		}
		runner := &Compose{CLIRunner: &CLIRunner{Bin: bin, Base: c.base}, Label: c.label}
		if probeCompose(ctx, runner) {
			saveComposeCache(runtimeName, fingerprint, runner)
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

// composePluginDirs are where docker (and podman, for its compose provider)
// look for the docker compose CLI plugin, which no PATH lookup sees.
var composePluginDirs = []string{
	"/usr/local/lib/docker/cli-plugins",
	"/usr/local/libexec/docker/cli-plugins",
	"/usr/lib/docker/cli-plugins",
	"/usr/libexec/docker/cli-plugins",
}

// composeFingerprint identifies the installed compose implementations: the
// path, size and mtime of every candidate binary on PATH and of the docker
// compose plugin, plus the env that steers `podman compose`'s choice of
// provider. Installing, removing or upgrading any of them changes it, which
// invalidates the cached probe result. Stats only - no process is run.
func composeFingerprint(cands []composeCandidate) string {
	var b strings.Builder
	stat := func(p string) {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d\n", p, st.Size(), st.ModTime().UnixNano())
		}
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.bin] {
			continue
		}
		seen[c.bin] = true
		if p, err := exec.LookPath(c.bin); err == nil {
			stat(p)
		} else {
			fmt.Fprintf(&b, "%s:-\n", c.bin)
		}
	}
	dirs := composePluginDirs
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		dirs = append([]string{filepath.Join(d, "cli-plugins")}, dirs...)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append([]string{filepath.Join(home, ".docker", "cli-plugins")}, dirs...)
	}
	for _, d := range dirs {
		stat(filepath.Join(d, "docker-compose"))
	}
	fmt.Fprintf(&b, "PODMAN_COMPOSE_PROVIDER=%s\n", os.Getenv("PODMAN_COMPOSE_PROVIDER"))
	fmt.Fprintf(&b, "PATH=%s\n", os.Getenv("PATH"))
	return b.String()
}

// composeCacheEntry is the on-disk record of a successful probe.
type composeCacheEntry struct {
	Fingerprint string   `json:"fingerprint"`
	Bin         string   `json:"bin"`
	Base        []string `json:"base"`
	Label       string   `json:"label"`
}

// composeCachePath is the cache file for the given runtime's probe (the
// candidate order, and so the winner, depends on the runtime).
func composeCachePath(runtimeName string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "devc", "compose-"+filepath.Base(runtimeName)+".json"), nil
}

// loadComposeCache returns the cached compose implementation, or nil when there
// is none or it was recorded for a different fingerprint.
func loadComposeCache(runtimeName, fingerprint string) *Compose {
	p, err := composeCachePath(runtimeName)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var e composeCacheEntry
	if json.Unmarshal(b, &e) != nil || e.Fingerprint != fingerprint || e.Bin == "" {
		return nil
	}
	return &Compose{CLIRunner: &CLIRunner{Bin: e.Bin, Base: e.Base}, Label: e.Label}
}

// saveComposeCache records a successful probe. Best-effort: a failure just
// means the next run probes again.
func saveComposeCache(runtimeName, fingerprint string, c *Compose) {
	p, err := composeCachePath(runtimeName)
	if err != nil {
		return
	}
	b, err := json.Marshal(composeCacheEntry{Fingerprint: fingerprint, Bin: c.Bin, Base: c.Base, Label: c.Label})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".compose-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
	}
}
