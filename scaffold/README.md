# Blocks

`devc init` builds a `.devcontainer/` from blocks. A block is one topic (`go`,
`claude`, `gitea`, ...) and contributes a piece to each generated file. The
result is plain files the project owns; devc's runtime knows nothing about
blocks.

```
blocks/<name>/
  block.jsonc          metadata (required)
  Containerfile        appended as its own section (optional)
  devcontainer.jsonc   merged into devcontainer.json (optional)
  post-create.sh       appended to post-create.sh (optional)
presets/<name>.jsonc   a named list of blocks with parameter values
```

A preset:

```jsonc
{
	"description": "Go service with Claude Code",
	"blocks": ["go", "claude"],
	// Optional, per block.
	"params": { "go": { "gopls": "v0.23.0" } }
}
```

## block.jsonc

```jsonc
{
	"description": "Go toolchain and gopls",
	// Exactly one base block per environment; it provides FROM.
	"base": false,
	// Blocks this one needs. They are added automatically and come first.
	"requires": ["fedora"],
	"params": {
		"version": { "description": "Node major version", "default": "24" }
	}
}
```

A param without `default` must be given.

## Fragments

All fragment files are Go `text/template`s over the block's params
(`{{.version}}`). In `devcontainer.jsonc` values are JSON-escaped; in
`post-create.sh` they are inserted as-is, so quote them there. Comments are
part of the fragment and end up in the output: they carry the "why".

- **Containerfile:** the base block's fragment comes first and holds `FROM`.
  Every other block gets its own section with its own `RUN`, in dependency
  order.
- **devcontainer.jsonc:** objects merge key by key, arrays are concatenated
  without duplicates, and two blocks setting the same scalar to different values
  is an error. When two blocks fill the same key, the comments above
  that key move down to the first entry each block added.
- **post-create.sh:** if any block has one, `postCreateCommand` is set to
  `.devcontainer/post-create.sh`. Fragments run in dependency order in one
  script with `set -euo pipefail`, from the workspace folder. Each fragment must be
  idempotent. A fragment that wants to react to another tool checks for it at
  runtime (`command -v go`) instead of in the template.

Blocks target the single-container form. Compose stacks stay hand-written.
