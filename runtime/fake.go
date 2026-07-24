package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FakeRunner is a Runner that records every invocation and replays scripted
// responses. It lets the whole tool be tested without a container runtime:
// argv is asserted against goldens, and Inspect / Output results are canned.
type FakeRunner struct {
	Bin string

	// Calls records the argv of every Run/Output/Inspect invocation, in order.
	Calls [][]string

	// OutputFunc, if set, produces the stdout (and optional error) for an
	// Output/Inspect call given its argv. Defaults to empty output, no error.
	OutputFunc func(args []string) ([]byte, error)

	// RunErr, if set, is returned by Run.
	RunErr error
}

// NewFake returns a FakeRunner named "podman" by default.
func NewFake() *FakeRunner { return &FakeRunner{Bin: "podman"} }

func (f *FakeRunner) Name() string {
	if f.Bin == "" {
		return "podman"
	}
	return f.Bin
}

func (f *FakeRunner) record(args []string) {
	cp := make([]string, len(args))
	copy(cp, args)
	f.Calls = append(f.Calls, cp)
}

func (f *FakeRunner) Run(_ context.Context, args []string, _ IO) error {
	f.record(args)
	return f.RunErr
}

func (f *FakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.record(args)
	if f.OutputFunc != nil {
		return f.OutputFunc(args)
	}
	return nil, nil
}

func (f *FakeRunner) Inspect(_ context.Context, ref string, v any) error {
	args := []string{"inspect", "--format", "{{json .}}", ref}
	f.record(args)
	if f.OutputFunc == nil {
		return fmt.Errorf("%q: %w", ref, ErrNoSuchObject)
	}
	out, err := f.OutputFunc(args)
	if err != nil {
		return err
	}
	if len(out) == 0 {
		return fmt.Errorf("%q: %w", ref, ErrNoSuchObject)
	}
	return json.Unmarshal(out, v)
}

// LastCall returns the argv of the most recent invocation, or nil.
func (f *FakeRunner) LastCall() []string {
	if len(f.Calls) == 0 {
		return nil
	}
	return f.Calls[len(f.Calls)-1]
}

// CallStrings renders every recorded call as a space-joined line, for golden
// comparison and debugging.
func (f *FakeRunner) CallStrings() []string {
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

// FindCall returns the first recorded call whose first argument equals verb, or
// nil. Handy for asserting a specific subcommand's flags.
func (f *FakeRunner) FindCall(verb string) []string {
	for _, c := range f.Calls {
		if len(c) > 0 && c[0] == verb {
			return c
		}
	}
	return nil
}
