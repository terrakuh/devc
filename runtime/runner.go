// Package runtime abstracts the container CLI (podman or docker) behind a small
// Runner interface. Every higher-level operation - build, run, exec, cp,
// compose - is argv construction on top of Runner, which makes the whole tool
// testable with a FakeRunner that records argv and replays canned inspect JSON,
// with no container runtime present.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// IO carries the standard streams for a single invocation. Any nil stream is
// left disconnected (the child gets no stdin / its output is discarded).
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs one container-CLI invocation at a time. Implementations must be
// safe for sequential use; devc never shares a Runner across goroutines.
type Runner interface {
	// Name is the runtime binary, "podman" or "docker".
	Name() string
	// Run executes `<name> <args...>` wired to io and returns the process
	// error (an *exec.ExitError on non-zero exit).
	Run(ctx context.Context, args []string, io IO) error
	// Output runs `<name> <args...>` and returns stdout. Stderr is captured and
	// folded into the error on failure.
	Output(ctx context.Context, args ...string) ([]byte, error)
	// Inspect runs `<name> inspect --format {{json .}} <ref>` and decodes the
	// single-object result into v. It returns ErrNoSuchObject if ref is unknown.
	Inspect(ctx context.Context, ref string, v any) error
}

// ErrNoSuchObject is returned by Inspect when the reference does not exist.
var ErrNoSuchObject = fmt.Errorf("no such object")

// CLIRunner is the real Runner, shelling out to a container CLI on PATH.
type CLIRunner struct {
	// Bin is the executable name or path ("podman", "docker", or absolute).
	Bin string
	// Base is prepended to every argument list (unused today; reserved for
	// global flags such as --connection). Kept so callers need not special-case.
	Base []string
}

// NewCLIRunner returns a CLIRunner for the given binary.
func NewCLIRunner(bin string) *CLIRunner { return &CLIRunner{Bin: bin} }

func (r *CLIRunner) Name() string {
	// Report the logical name even when Bin is an absolute path.
	if i := strings.LastIndexByte(r.Bin, '/'); i >= 0 {
		return r.Bin[i+1:]
	}
	return r.Bin
}

// BinPath returns the runner's executable path, absolutized when possible. It is
// baked into the generated ssh ProxyCommand so the transport does not re-resolve
// the runtime through a possibly-stripped PATH.
func (r *CLIRunner) BinPath() string {
	if abs, err := exec.LookPath(r.Bin); err == nil {
		if p, err := filepath.Abs(abs); err == nil {
			return p
		}
		return abs
	}
	if p, err := filepath.Abs(r.Bin); err == nil {
		return p
	}
	return r.Bin
}

func (r *CLIRunner) argv(args []string) []string {
	out := make([]string, 0, len(r.Base)+len(args))
	out = append(out, r.Base...)
	out = append(out, args...)
	return out
}

func (r *CLIRunner) Run(ctx context.Context, args []string, io IO) error {
	cmd := exec.CommandContext(ctx, r.Bin, r.argv(args)...)
	cmd.Stdin = io.Stdin
	cmd.Stdout = io.Stdout
	cmd.Stderr = io.Stderr
	return cmd.Run()
}

func (r *CLIRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Bin, r.argv(args)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return stdout.Bytes(), fmt.Errorf("%s %s: %w", r.Name(), strings.Join(args, " "), err)
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", r.Name(), strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

func (r *CLIRunner) Inspect(ctx context.Context, ref string, v any) error {
	out, err := r.Output(ctx, "inspect", "--format", "{{json .}}", ref)
	if err != nil {
		if isNoSuchObject(err) {
			return fmt.Errorf("%q: %w", ref, ErrNoSuchObject)
		}
		return err
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return fmt.Errorf("%q: %w", ref, ErrNoSuchObject)
	}
	if err := json.Unmarshal(trimmed, v); err != nil {
		return fmt.Errorf("decode inspect of %q: %w", ref, err)
	}
	return nil
}

// isNoSuchObject recognizes the "not found" message from both podman and docker.
func isNoSuchObject(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such object") ||
		strings.Contains(s, "no such container") ||
		strings.Contains(s, "no such image") ||
		strings.Contains(s, "error: no such") ||
		strings.Contains(s, "unable to find")
}
