//go:build devc_integration

// Integration test for the full ssh path against a real container runtime. It is
// excluded from normal `go test` by the devc_integration build tag; run it on a
// host that has podman (or docker) and ssh:
//
//	go test -tags devc_integration ./cmd/devc/ -run TestSSHRoundTrip -v
//
// It builds the devc binary, brings up a throwaway single-container workspace,
// and asserts that `ssh <alias>` reaches the injected agent: command exit codes,
// output, and a local port-forward all work - the same things VSCodium relies on.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("no container runtime on PATH")
		}
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not on PATH")
	}

	// Isolate host-side state and ssh config so the test never touches the real ~/.
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)

	// Build devc.
	devcBin := filepath.Join(t.TempDir(), "devc")
	build := exec.Command("go", "build", "-o", devcBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build devc: %v", err)
	}

	// A minimal single-container workspace kept alive by overrideCommand.
	proj := t.TempDir()
	dc := `{
		"name": "itest",
		"image": "docker.io/library/alpine:3.20",
		"remoteUser": "root",
		"workspaceFolder": "/work"
	}`
	if err := os.WriteFile(filepath.Join(proj, ".devcontainer.json"), []byte(dc), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		cmd := exec.Command(devcBin, args...)
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("up", "--path", proj); err != nil {
		t.Fatalf("devc up: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = run("down", "--path", proj, "--purge") })

	alias := "devc.itest"
	sshArgs := []string{
		"-F", filepath.Join(home, "config", "devc", "ssh.config"),
		"-o", "ConnectTimeout=10",
		alias,
	}

	// exit code propagates
	cmd := exec.Command(sshBin, append(sshArgs, "exit 7")...)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit from `exit 7`")
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("want exit 7, got %v", err)
	}

	// output + cwd
	cmd = exec.Command(sshBin, append(sshArgs, "pwd; echo marker-$((1+1))")...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh echo: %v", err)
	}
	if !strings.Contains(string(out), "/work") || !strings.Contains(string(out), "marker-2") {
		t.Fatalf("unexpected ssh output: %q", out)
	}

	_ = time.Second
}
