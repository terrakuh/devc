package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/terrakuh/devc/agent"
	"github.com/terrakuh/devc/keys"
)

// version is the devc/agent version. It gates agent re-injection: a container
// whose /.devc/agent reports this string is left untouched.
//
// It is normally left at the default and filled by initVersion from the build
// info: `go install .../cmd/devc@vX` and clean release-tag builds both report the
// tag, other builds report a VCS pseudo-version. A linker override still wins:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "0.0.0"

func init() { initVersion() }

// initVersion fills version from the build info when it was not overridden by the
// linker. Go stamps the main module version from VCS, so a clean checkout on a
// tag yields that tag; anything else yields a pseudo-version. An explicit -X
// value, or the default when no build info is available, is left untouched.
func initVersion() {
	if version != "0.0.0" {
		return // overridden with -ldflags -X
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
}

// runServe is the hidden in-container entrypoint. It runs the SSH server over
// stdin/stdout (the podman-exec pipe). Never invoked by users directly; devc's
// ProxyCommand launches it via `exec -i <ctr> /.devc/agent __serve ...`.
func runServe(args []string) error {
	fs := flag.NewFlagSet("__serve", flag.ContinueOnError)
	hostKey := fs.String("host-key", "", "path to the agent host key")
	authorized := fs.String("authorized", "", "path to the authorized client public key(s)")
	envFile := fs.String("env-file", "", "path to a KEY=VALUE env file injected into sessions")
	user := fs.String("user", "", "OS user to run sessions as")
	cwd := fs.String("cwd", "", "working directory for sessions")
	forwardAgent := fs.Bool("forward-agent", false, "allow ssh-agent forwarding (auth-agent-req)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hostKey == "" || *authorized == "" {
		return fmt.Errorf("__serve requires --host-key and --authorized")
	}

	signer, err := keys.LoadSigner(*hostKey)
	if err != nil {
		return fmt.Errorf("load host key: %w", err)
	}
	authKeys, err := keys.LoadAuthorized(*authorized)
	if err != nil {
		return fmt.Errorf("load authorized keys: %w", err)
	}

	srv, err := agent.NewServer(agent.Config{
		HostKey:         signer,
		Authorized:      authKeys,
		User:            *user,
		Cwd:             *cwd,
		Environment:     readEnvFile(*envFile),
		AgentForwarding: *forwardAgent,
	})
	if err != nil {
		return err
	}
	// Serve one connection over the exec pipe, then exit.
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}

// runVersion prints the agent version (used both by the CLI and by injection's
// up-to-date check inside the container).
func runVersion() error {
	fmt.Println(version)
	return nil
}

// readEnvFile parses a KEY=VALUE file (one per line); missing file -> empty map.
func readEnvFile(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	f, err := os.Open(path) //nolint:gosec // path is devc-controlled
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}
