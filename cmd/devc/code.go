package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/terrakuh/devc/config"
)

// editorCandidates are the Remote-SSH-capable editor launchers devc tries, in
// order of preference: VSCodium first, then VS Code (and its insiders build).
// Override the choice with `--editor` or $DEVC_EDITOR.
var editorCandidates = []string{"codium", "code", "code-insiders"}

// runCode implements `devc code [flags]`: open the workspace in VSCodium
// (preferred) or VS Code over Remote-SSH. It launches the editor with a
// folder-uri pointing at the workspace's ssh alias and container folder, e.g.
//
//	codium --folder-uri vscode-remote://ssh-remote+devc.<name><containerWorkspaceFolder>
//
// The editor connects through the ssh alias `devc up` wrote, whose ProxyCommand
// starts the container on demand, so this works whether or not it is running.
func runCode(args []string) error {
	fs := flag.NewFlagSet("code", flag.ContinueOnError)
	var cf commonFlags
	editor := fs.String("editor", os.Getenv("DEVC_EDITOR"), "editor launcher to use (default: codium, else code)")
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	e, err := setup(ctx, &cf)
	if err != nil {
		return err
	}

	bin, err := resolveEditor(*editor)
	if err != nil {
		return err
	}

	uri := folderURI(e.spec)
	if !cf.quiet {
		fmt.Fprintf(os.Stderr, "opening %s in %s\n", uri, bin)
	}

	// The launcher signals an existing window (or spawns and detaches) and
	// returns promptly, so Run is fine and surfaces a launch failure.
	cmd := exec.Command(bin, "--folder-uri", uri) //nolint:gosec // bin resolved via LookPath
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// resolveEditor finds the editor launcher on PATH. An explicit choice (flag or
// $DEVC_EDITOR) must exist; otherwise the first available candidate wins.
func resolveEditor(explicit string) (string, error) {
	if explicit != "" {
		bin, err := exec.LookPath(explicit)
		if err != nil {
			return "", fmt.Errorf("editor %q not found on PATH: %w", explicit, err)
		}
		return bin, nil
	}
	for _, c := range editorCandidates {
		if bin, err := exec.LookPath(c); err == nil {
			return bin, nil
		}
	}
	return "", fmt.Errorf("no editor found on PATH (tried %v); set --editor or $DEVC_EDITOR", editorCandidates)
}

// folderURI builds the Remote-SSH folder URI for a workspace: the ssh alias
// devc generated, joined with the container workspace folder.
func folderURI(spec *config.Spec) string {
	return "vscode-remote://ssh-remote+" + sshAlias(spec) + spec.ContainerWorkspaceFolder
}
