package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
)

// check is one diagnostic line.
type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
}

// runDoctor implements `devc doctor`: preflight checks that turn a confusing
// VSCodium connect failure into one clear message. It verifies the runtime, the
// container, and the handful of in-container tools the editor's server installer
// needs (tar, curl/wget), the libc flavour, $HOME, free space, and the agent.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var cf commonFlags
	jsonOut := fs.Bool("json", false, "emit the checks as JSON")
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	checks := runChecks(ctx, e)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(checks)
	}
	printChecks(checks)
	// A failed check is a non-zero exit so scripts can gate on `devc doctor`.
	for _, c := range checks {
		if c.Status == statusFail {
			return fmt.Errorf("doctor found problems")
		}
	}
	return nil
}

// runChecks executes every diagnostic and returns them in display order.
func runChecks(ctx context.Context, e *env) []check {
	var checks []check
	add := func(name string, status checkStatus, detail string) {
		checks = append(checks, check{Name: name, Status: status, Detail: detail})
	}

	// Runtime is on the host and already selected; report its version.
	if v, err := runtime.Version(ctx, e.runner); err == nil {
		add("runtime", statusOK, v)
	} else {
		add("runtime", statusWarn, e.runner.Name()+" (version unavailable)")
	}

	// Compose implementation, only relevant for the compose kind.
	if e.spec.Kind == config.KindCompose {
		if comp, err := e.composeImpl(ctx); err == nil {
			add("compose", statusOK, comp.Label)
		} else {
			add("compose", statusFail, err.Error())
		}
	}

	// Everything below needs a running container.
	ref, info, err := e.attachRef(ctx)
	if err != nil {
		add("container", statusFail, err.Error())
		return checks
	}
	if info == nil || !info.Running() {
		add("container", statusFail, "not running - run `devc up`")
		return checks
	}
	add("container", statusOK, "running ("+short(info.ID)+")")

	// In-container tool probes, as the session user the editor will use.
	tool := func(name, cmd string) {
		if out, ok := e.probe(ctx, ref, "command -v "+cmd); ok && out != "" {
			add(name, statusOK, out)
		} else {
			add(name, statusFail, cmd+" not found (the editor server installer needs it)")
		}
	}
	tool("shell", "sh")
	tool("tar", "tar")

	if out, ok := e.probe(ctx, ref, "command -v curl || command -v wget"); ok && out != "" {
		add("http-client", statusOK, out)
	} else {
		add("http-client", statusFail, "neither curl nor wget found (needed to download the editor server)")
	}

	if out, ok := e.probe(ctx, ref, "ldd --version 2>&1 | head -1"); ok {
		if strings.Contains(strings.ToLower(out), "musl") {
			add("libc", statusWarn, "musl detected - the editor needs its musl server build; some extensions may not load")
		} else {
			add("libc", statusOK, firstLine(out))
		}
	}

	if home, ok := e.probe(ctx, ref, `test -w "$HOME" && printf %s "$HOME"`); ok && home != "" {
		add("home", statusOK, home+" (writable)")
	} else {
		add("home", statusFail, "$HOME is missing or not writable for the session user")
	}

	if out, ok := e.probe(ctx, ref, `df -Pk "$HOME" 2>/dev/null | awk 'NR==2{print $4}'`); ok {
		if kb, perr := strconv.Atoi(strings.TrimSpace(out)); perr == nil {
			detail := fmt.Sprintf("%d MB free in $HOME", kb/1024)
			if kb < 200*1024 { // the editor server is ~150 MB
				add("disk", statusWarn, detail+" (the editor server is ~150 MB)")
			} else {
				add("disk", statusOK, detail)
			}
		}
	}

	// Agent presence and version match.
	if out, ok := e.probe(ctx, ref, container.AgentBinary+" version 2>/dev/null"); ok && out != "" {
		if strings.TrimSpace(out) == version {
			add("agent", statusOK, "version "+out)
		} else {
			add("agent", statusWarn, fmt.Sprintf("version %s, expected %s - run `devc up` to refresh", out, version))
		}
	} else {
		add("agent", statusFail, "not installed - run `devc up`")
	}

	return checks
}

// probe runs `sh -c script` inside the container as the session user and returns
// trimmed stdout and whether it exited zero. It is best-effort: a non-zero exit
// (a missing tool) is a normal "false", not an error to surface.
func (e *env) probe(ctx context.Context, ref, script string) (string, bool) {
	argv := container.ExecArgs(ref, e.spec.RemoteUser, "", nil, false, false, []string{"sh", "-c", script})
	out, err := e.runner.Output(ctx, argv...)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// printChecks renders the checks as an aligned, symbol-prefixed list.
func printChecks(checks []check) {
	sym := map[checkStatus]string{statusOK: "✓", statusWarn: "!", statusFail: "✗"}
	width := 0
	for _, c := range checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range checks {
		fmt.Printf("%s %-*s  %s\n", sym[c.Status], width, c.Name, c.Detail)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
