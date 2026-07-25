// Command devc is a minimal devcontainer runner: it reads a devcontainer.json
// (compose or single-container form, no features/customizations), brings the
// container(s) up with podman or docker-compose, and exposes an SSH endpoint a
// local editor (VSCodium Remote-SSH) can connect to. Think devpod, much smaller.
//
// This is a standalone development tool; it has nothing to do with the shop
// product that also lives in this module.
//
// The binary doubles as the in-container SSH agent (the hidden __serve/__sftp
// subcommands), so it must be built static: CGO_ENABLED=0. See devc/README.md
// for the full command surface, config support, and troubleshooting.
package main

import (
	"fmt"
	"os"
)

const usage = `devc - a minimal devcontainer runner

Usage:
  devc <command> [flags]

Commands:
  up          Create/start the workspace container(s)
  down        Remove the workspace container(s)
  stop        Stop the workspace without removing it
  restart     Restart the workspace's main service (compose: --all for every service)
  status      Show the workspace's state
  list        List all devc workspaces on this host
  logs        Show container (or compose) logs
  exec        Run a command inside the workspace container
  ssh         SSH into the workspace (spawn ssh, or --stdio ProxyCommand)
  code        Open the workspace in VSCodium/VS Code over Remote-SSH
  ssh-config  Regenerate or print the workspace's ssh config block
  keys        Show or rotate the workspace's SSH keys
  doctor      Diagnose a workspace (runtime, tools, agent) for SSH/editor use
  config      Load and print the resolved devcontainer configuration
  help        Show this help

Run "devc <command> -h" for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "config":
		err = runConfig(args)
	case "up":
		err = runUp(args)
	case "down":
		err = runDown(args)
	case "stop":
		err = runStop(args)
	case "restart":
		err = runRestart(args)
	case "status":
		err = runStatus(args)
	case "list":
		err = runList(args)
	case "logs":
		err = runLogs(args)
	case "exec":
		err = runExec(args)
	case "ssh":
		err = runSSH(args)
	case "code":
		err = runCode(args)
	case "ssh-config":
		err = runSSHConfig(args)
	case "keys":
		err = runKeys(args)
	case "doctor":
		err = runDoctor(args)
	case "__serve":
		err = runServe(args)
	case "__sftp":
		err = runSFTP()
	case "version":
		err = runVersion()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "devc: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "devc: %v\n", err)
		os.Exit(1)
	}
}
