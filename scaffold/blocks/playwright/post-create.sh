# The browser version is pinned by each project's @playwright/test dependency,
# so it is fetched here and not baked into the image. Easy to miss: a test file
# whose fixture cannot launch Chromium counts its tests as skipped, not failed.
for dir in {{.dirs}}; do
	if [[ ! -x "$dir/node_modules/.bin/playwright" ]]; then
		echo "WARNING: $dir has no node_modules yet. Run 'pnpm install' there, then this script again." >&2
		continue
	fi
	(cd "$dir" && pnpm exec playwright install chromium)
done
