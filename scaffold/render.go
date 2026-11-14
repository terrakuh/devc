package scaffold

import (
	"fmt"
	"strings"
	"text/template"
)

// File is one generated file, relative to the .devcontainer folder.
type File struct {
	Name       string
	Data       []byte
	Executable bool
}

const rule = "# ---------------------------------------------------------------------------\n"

// Render produces devcontainer.json, Containerfile and, if any block has one,
// post-create.sh. name becomes the devcontainer.json "name".
func (s *Selection) Render(name string) ([]File, error) {
	names := make([]string, len(s.Blocks))
	for i, b := range s.Blocks {
		names[i] = b.Name
	}
	origin := fmt.Sprintf("Created by `devc init` (blocks: %s). Edit freely.", strings.Join(names, ", "))

	root := &node{kind: object, from: "devc init", members: []*member{
		{key: "name", value: &node{kind: scalar, raw: quote(name), from: "devc init"}},
	}}
	var cf, pc strings.Builder
	cf.WriteString("# " + origin + "\n\n")

	for _, b := range s.Blocks {
		params := s.Params[b.Name]
		if b.containerfile != nil {
			text, err := execute(b, b.containerfile, params)
			if err != nil {
				return nil, err
			}
			if !b.Base {
				cf.WriteString("\n" + rule + "# " + b.Name + ": " + b.Description + "\n" + rule)
			}
			cf.WriteString(text)
		}
		if b.devcontainer != nil {
			text, err := execute(b, b.devcontainer, jsonEscaped(params))
			if err != nil {
				return nil, err
			}
			frag, err := parseTree(text, b.Name)
			if err != nil {
				return nil, fmt.Errorf("block %s: devcontainer.jsonc: %w", b.Name, err)
			}
			if frag.kind != object {
				return nil, fmt.Errorf("block %s: devcontainer.jsonc: not an object", b.Name)
			}
			if err := merge(root, frag, ""); err != nil {
				return nil, err
			}
		}
		if b.postCreate != nil {
			text, err := execute(b, b.postCreate, params)
			if err != nil {
				return nil, err
			}
			pc.WriteString("\n" + rule + "# " + b.Name + ": " + b.Description + "\n" + rule + text)
		}
	}

	files := []File{{Name: "Containerfile", Data: []byte(cf.String())}}
	if pc.Len() > 0 {
		hook := &node{kind: object, from: "devc init", members: []*member{
			{key: "postCreateCommand", value: &node{kind: scalar, raw: quote(".devcontainer/post-create.sh"), from: "devc init"}},
		}}
		if err := merge(root, hook, ""); err != nil {
			return nil, err
		}
		script := "#!/usr/bin/env bash\n" +
			"# Runs once after the container is created (postCreateCommand). Every section\n" +
			"# is safe to run again.\n" +
			"# " + origin + "\n" +
			"set -euo pipefail\n" +
			"cd \"$(dirname \"$0\")/..\"\n" +
			pc.String()
		files = append(files, File{Name: "post-create.sh", Data: []byte(script), Executable: true})
	}
	devcontainer := "// " + origin + "\n" + root.format()
	return append([]File{{Name: "devcontainer.json", Data: []byte(devcontainer)}}, files...), nil
}

// execute renders a fragment; the result always ends in exactly one newline.
func execute(b *Block, t *template.Template, params map[string]string) (string, error) {
	var out strings.Builder
	if err := t.Execute(&out, params); err != nil {
		return "", fmt.Errorf("block %s: %w", b.Name, err)
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// jsonEscaped escapes values for use inside a JSON string literal.
func jsonEscaped(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		q := quote(v)
		out[k] = q[1 : len(q)-1]
	}
	return out
}
