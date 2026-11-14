if [ -z "${GITEA_SERVER_TOKEN:-}" ]; then
	echo "WARNING: {{.tokenEnv}} is not set on the host; skipping tea login." >&2
elif ! tea login list 2>/dev/null | grep -qw '{{.login}}'; then
	tea login add --name '{{.login}}'
fi
