package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Kind names a container runtime.
type Kind string

const (
	Podman Kind = "podman"
	Docker Kind = "docker"
)

// Detect picks a container runtime. Selection order:
//
//	prefer (from --runtime flag) -> DEVC_RUNTIME env -> autodetect
//
// Autodetection prefers podman over docker. It returns the binary found on
// PATH; an explicit preference that is not installed is an error.
func Detect(prefer string) (*CLIRunner, error) {
	if prefer == "" {
		prefer = os.Getenv("DEVC_RUNTIME")
	}
	if prefer != "" {
		bin, err := exec.LookPath(prefer)
		if err != nil {
			return nil, fmt.Errorf("requested runtime %q not found on PATH: %w", prefer, err)
		}
		return NewCLIRunner(bin), nil
	}
	for _, cand := range []string{string(Podman), string(Docker)} {
		if bin, err := exec.LookPath(cand); err == nil {
			return NewCLIRunner(bin), nil
		}
	}
	return nil, fmt.Errorf("no container runtime found on PATH (looked for podman, docker); set --runtime or DEVC_RUNTIME")
}

// IsRootlessPodman reports whether r is podman running rootless, which decides
// defaults for --userns=keep-id and SELinux handling. It shells out to
// `podman info` and tolerates any failure by returning false.
func IsRootlessPodman(ctx context.Context, r Runner) bool {
	if r.Name() != string(Podman) {
		return false
	}
	out, err := r.Output(ctx, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Version returns the runtime's version string (`<bin> --version`), trimmed.
func Version(ctx context.Context, r Runner) (string, error) {
	out, err := r.Output(ctx, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
