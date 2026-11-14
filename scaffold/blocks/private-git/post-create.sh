# git config and go env live in the container's home, which a rebuild throws
# away, so this runs on every new container.
GIT_HOST={{.host}}
GIT_SSH_PORT={{.sshPort}}

# Tools resolve modules over https://$GIT_HOST/; rewrite that to the SSH remote
# so the forwarded agent supplies the key and nothing prompts for a password.
git config --global "url.ssh://git@${GIT_HOST}:${GIT_SSH_PORT}/.insteadOf" "https://${GIT_HOST}/"

# Keep go off the module proxy and checksum db for the private host.
if command -v go >/dev/null; then
	go env -w GOPRIVATE="${GIT_HOST}"
fi

# Pin the host key so the first fetch does not block on a prompt.
mkdir -p ~/.ssh && chmod 700 ~/.ssh
touch ~/.ssh/known_hosts && chmod 600 ~/.ssh/known_hosts
if [ "$GIT_SSH_PORT" = 22 ]; then KNOWN_HOST=$GIT_HOST; else KNOWN_HOST="[$GIT_HOST]:$GIT_SSH_PORT"; fi
{{- if .hostKey}}
HOST_KEY="$KNOWN_HOST {{.hostKey}}"
if ! grep -qxF "$HOST_KEY" ~/.ssh/known_hosts; then
	printf '%s\n' "$HOST_KEY" >>~/.ssh/known_hosts
fi
{{- else}}
if ! ssh-keygen -F "$KNOWN_HOST" >/dev/null; then
	ssh-keyscan -p "$GIT_SSH_PORT" "$GIT_HOST" >>~/.ssh/known_hosts 2>/dev/null ||
		echo "WARNING: could not fetch the host key of $GIT_HOST." >&2
fi
{{- end}}

# Non-fatal: without a key in the agent the container still works.
if ! ssh -T -p "$GIT_SSH_PORT" -o BatchMode=yes -o ConnectTimeout=5 "git@$GIT_HOST" </dev/null 2>&1 |
	grep -qi 'authenticated\|successfully\|welcome'; then
	echo "WARNING: could not authenticate to git@$GIT_HOST:$GIT_SSH_PORT. Is your key loaded in the ssh-agent?" >&2
fi
