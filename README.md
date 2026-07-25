# devc - a minimal devcontainer runner

`devc` reads a `devcontainer.json`, brings the container(s) up with **podman** or
**docker**, and gives you an SSH host an editor's Remote-SSH can connect to
(VSCodium/VS Code). Think [devpod](https://devpod.sh), without the provider
abstraction, features, or cloud. It is a standalone tool.

```shell
cd ~/src/myproject
devc up      # build/start, then write ~/.config/devc/ssh.config
             # now: ssh devc.myproject  (or point Remote-SSH at devc.myproject)
devc down    # remove the container(s)
```

---

## Install

With Go (installs a binary named `devc`):

```shell
CGO_ENABLED=0 go install github.com/terrakuh/devc/cmd/devc@latest
```

`CGO_ENABLED=0` is required: the binary injects itself into arbitrary container
images, so it must be static (see [How SSH works](#how-ssh-works)). Use `@latest`
or pin a tag like `@v1.0.0`. The version is read from the build info, so a tagged
install (or a clean release-tag build) reports that tag with no extra flags.

Or build from source:

```shell
CGO_ENABLED=0 go build -o devc ./cmd/devc

# smaller, reproducible binary:
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o devc ./cmd/devc
```

The host binary and the container must share a CPU architecture (there is no
cross-build); `devc doctor` reports a mismatch. Prebuilt static linux binaries
for amd64 and arm64 are attached to each GitHub release.

---

## How SSH works

There is **no sshd in the image and no published port.** The `devc` binary
injects _itself_ into the container as an SSH server and talks the SSH protocol
over a `podman exec` pipe. You are already allowed to reach the container (you
can run the container runtime), so no network auth is needed. The SSH crypto
still runs because a real editor is a real SSH client, but the keys are generated
per workspace, stay on your host, and never leave the machine.

```
ssh devc.myproject
  -> ProxyCommand: devc ssh --stdio --start --config <abs>/devcontainer.json --runtime /usr/bin/podman myproject
       -> podman exec -i <ctr> /.devc/agent __serve   <-- SSH -->  in-container agent
                                                                     - session (shell/exec/pty/sftp)
                                                                     - direct-tcpip  (ssh -L)
                                                                     - tcpip-forward (ssh -R)
```

Because the binary runs inside an arbitrary image, it **must** be built static
(`CGO_ENABLED=0`) so it carries no dynamic libc dependency. See [Install](#install).

---

## Commands

| Command                          | What it does                                                                                                                          |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `devc up`                        | build/start, inject the agent, run hooks, write ssh config. Flags: `--recreate`, `--rebuild`, `--skip-hooks`, `--rerun-hooks`         |
| `devc down`                      | remove the container(s). `--volumes` (compose), `--purge` (also drop keys, ssh block, control dir), `--auto` (honor `shutdownAction`) |
| `devc stop`                      | stop without removing                                                                                                                 |
| `devc restart [--all]`           | restart the main service (compose: just the attach service; `--all` restarts every service)                                           |
| `devc status [--json]`           | this workspace: kind, runtime, state, ports, config drift                                                                             |
| `devc list [--json]`             | every devc workspace on the host (found by label)                                                                                     |
| `devc logs [--follow] [service]` | container / compose logs                                                                                                              |
| `devc exec [-T] -- <cmd>`        | run as the remote user in the workspace folder, with `remoteEnv`                                                                      |
| `devc code [--editor <bin>]`     | open the workspace in VSCodium (preferred) or VS Code over Remote-SSH                                                                 |
| `devc ssh [<name>]`              | spawn `ssh devc.<name>` (shares the ControlMaster)                                                                                    |
| `devc ssh --stdio [--start]`     | ProxyCommand transport (what the generated config runs)                                                                               |
| `devc ssh-config [--print]`      | regenerate (or preview) the workspace's ssh config block                                                                              |
| `devc keys [--rotate]`           | show or rotate the workspace's SSH keys                                                                                               |
| `devc doctor [--json]`           | preflight: runtime, container, `tar`/`curl`, libc, `$HOME`, disk, agent                                                               |
| `devc config [--raw]`            | print the resolved `Spec` (or the post-substitution raw doc)                                                                          |

Global flags: `--path`, `-n`/`--name`, `--config`, `--runtime`, `--compose-cmd`,
`--platform`, `--selinux`, `--userns`, `-q`, `--forward-agent`,
`--sync-git-config` (the last two override the matching
`customizations.devc.credentials` keys).

`-n`/`--name` targets a workspace by its `devc list` name (or id) instead of a
folder, so you can run e.g. `devc code -n shop` or `devc stop -n api` from
anywhere. It resolves the workspace via container labels, so it works for any
workspace that still has a container (running or stopped).

---

## Config support

Both forms of `devcontainer.json` are supported (JSONC: comments and trailing
commas allowed).

- **Compose:** `dockerComposeFile` (string or array, applied in order), `service`,
  `runServices`, `workspaceFolder`, `forwardPorts`.
- **Single container:** `image` or `build.{dockerfile,context,args,target,...}`,
  `workspaceMount`, `mounts`, `containerEnv`, `containerUser`, `runArgs`, `init`,
  `privileged`, `capAdd`, `securityOpt`, `appPort`.

Shared by both: `name`, `remoteUser`, `remoteEnv`, `overrideCommand`,
`shutdownAction`, `userEnvProbe`, `waitFor`, and all lifecycle hooks
(`initializeCommand` on the host; `onCreate`/`updateContent`/`postCreate` once per
container identity; `postStart` every start; `postAttach`). Variable substitution
covers `${localWorkspaceFolder}`, `${localEnv:VAR:default}`,
`${containerWorkspaceFolder}`, `${devcontainerId}`, and deferred
`${containerEnv:VAR}`.

devc reads its own options from `customizations.devc` (see
[Credential forwarding](#credential-forwarding)); other `customizations` entries
(e.g. `vscode`) are ignored.

**Not supported (by design):** `features`, `hostRequirements`, and
`updateRemoteUserUID` are rejected with a clear error. `portsAttributes` is
parsed and ignored. On the compose path devc **never
generates an override file**, so single-container-only keys used alongside
`dockerComposeFile` are rejected instead of silently dropped; the compose file
itself must keep the service alive (`command: sleep infinity`).

`forwardPorts` become `LocalForward` lines in the ssh config, so the ports work
the moment you connect, with no publishing and no host-port collisions.

---

## Credential forwarding

devc can make host credentials available in the container without copying them.
Both options are opt-in under `customizations.devc.credentials`:

```jsonc
{
  "image": "fedora:44",
  "customizations": {
    "devc": {
      "credentials": {
        "forwardAgent": true, // ssh-agent forwarding for git-over-ssh
        "syncGitConfig": true, // copy host git identity into the container
      },
    },
  },
}
```

Override either per-run without editing the file: `devc up --forward-agent`,
`devc up --sync-git-config=false`.

- **`forwardAgent`** turns on ssh-agent forwarding. The ssh config gets
  `ForwardAgent yes` and the injected agent exposes a proxy `SSH_AUTH_SOCK`
  inside the container. Signing requests are tunnelled back to your host agent,
  so the private key never enters the container and there is no ssh-agent (and no
  key) in the image. You need a running host agent with keys added
  (`ssh-add -l` should list them). No host agent means no `SSH_AUTH_SOCK` is set.
- **`syncGitConfig`** copies a small allowlist of host git settings (`user.name`,
  `user.email`, `user.signingkey`, `commit.gpgsign`, `tag.gpgsign`, `gpg.format`,
  `init.defaultBranch`) into the container's `~/.config/git/config` on `devc up`.
  That file sits below `~/.gitconfig` in git's precedence, so the container can
  still override it. It is best-effort: no host git or config just skips.

The `credentials` block is the place for future forwarding (gpg, git credential
helper, docker) to land.

---

## Fedora / rootless podman notes

- **SELinux:** bind mounts get `:z` automatically when `selinuxenabled` is true.
  Override with `--selinux=auto|z|Z|none`.
- **User mapping:** devc does _not_ default to `--userns=keep-id`. Under plain
  rootless podman, container UID 0 already maps to your host user, so files land
  as yours while root stays available for `/.devc` setup and the agent's privilege
  drop. Pass `--userns=keep-id` only for a fixed non-root container user.
- The agent installs and runs as `--user 0`, then drops to the session user
  itself (the same model as sshd).

---

## State and credentials

Per workspace, under `~/.local/share/devc/<id>/` (0700):

| File                 | Purpose                                                                |
| -------------------- | ---------------------------------------------------------------------- |
| `id_ed25519`(`.pub`) | client key; the public half is the container's authorized key          |
| `host_key`(`.pub`)   | the agent's host key (the only private half copied into the container) |
| `known_hosts`        | pinned, so `StrictHostKeyChecking yes` is honest                       |
| `state.json`         | container identity, hook records, probed env, agent version            |

The SSH ControlMaster socket lives under `/tmp/devc-<uid>/<id>/`. This short path
matters: the state dir is too deep for the ~104-char Unix socket limit, which
would silently break connection multiplexing. `~/.ssh/config` gets one idempotent
`Include ~/.config/devc/ssh.config` line (with a timestamped backup on first
write).

The workspace id is `<slug>-<sha256(abs folder)[:8]>`: stable across rebuilds and
unique across two checkouts of the same repo.

---

## Troubleshooting the editor connection

The generated `ProxyCommand` bakes in an **absolute `--config` and `--runtime`**
so it resolves correctly whatever working directory or `PATH` the editor spawns it
with (GUI editors give it neither of yours). If Remote-SSH fails with a generic
_"connection lost before handshake"_ / _"premature close"_, the editor has thrown
away the ProxyCommand's stderr, so devc logs every step of the `--stdio` transport
to a file:

```shell
cat /tmp/devc-$(id -u)/ssh-proxy.log
```

That shows how far it got: which runtime was resolved, the exact `podman exec`
line, and the exit error. Then `devc doctor` checks the in-container side (`tar`,
`curl`/`wget`, libc, `$HOME`, disk, agent version) that the editor's server
installer needs.

After upgrading devc, reconnect with a fresh container (`devc up --recreate`), and
clear a stale mux socket with `ssh -O exit devc.<name>` if needed.

---

## Testing

Everything except the container runtime is unit-tested with **no daemon present**:
config parsing (jsonc, substitution, resolution), argv construction
(`runtime.FakeRunner`), ssh config rendering, hooks, and the **agent over a real
`x/crypto/ssh` client** (publickey accept/reject, exec exit codes, PTY,
`direct-tcpip`, `tcpip-forward`, SFTP round-trip).

```shell
go test ./...                               # unit tests, no daemon needed
go test -tags devc_integration ./cmd/devc/  # end-to-end, needs podman
```

Verify the editor itself manually, once; it is not part of the automated suite.
