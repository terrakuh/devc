// Package scaffold composes a .devcontainer/ from blocks: small directories
// that each contribute a Containerfile section, a devcontainer.json fragment and
// a post-create.sh fragment. See README.md in this directory for the format.
package scaffold

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/terrakuh/devc/jsonc"
)

//go:embed blocks presets
var builtin embed.FS

// Builtin returns the blocks and presets that ship with devc.
func Builtin() (*Catalog, error) {
	return Load(builtin)
}

// Catalog is a set of blocks and presets.
type Catalog struct {
	Blocks  map[string]*Block
	Presets map[string]*Preset
}

// Block is one topic of a dev environment.
type Block struct {
	Name        string
	Description string
	// Base blocks provide FROM; an environment has exactly one.
	Base     bool
	Requires []string
	Params   []Param

	containerfile *template.Template
	devcontainer  *template.Template
	postCreate    *template.Template
}

// Param is a value a block's fragments are rendered with.
type Param struct {
	Name        string
	Description string
	// Default is nil for a required param.
	Default *string
}

// Preset is a named list of blocks with parameter values.
type Preset struct {
	Name        string
	Description string                       `json:"description"`
	Blocks      []string                     `json:"blocks"`
	Params      map[string]map[string]string `json:"params"`
}

// Load reads blocks/<name>/ and presets/<name>.jsonc from fsys.
func Load(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{Blocks: map[string]*Block{}, Presets: map[string]*Preset{}}

	entries, err := fs.ReadDir(fsys, "blocks")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := loadBlock(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("block %s: %w", e.Name(), err)
		}
		c.Blocks[b.Name] = b
	}
	for _, b := range c.Blocks {
		for _, r := range b.Requires {
			if c.Blocks[r] == nil {
				return nil, fmt.Errorf("block %s: requires unknown block %q", b.Name, r)
			}
		}
	}

	entries, err = fs.ReadDir(fsys, "presets")
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsonc")
		if e.IsDir() || !ok {
			continue
		}
		p := &Preset{Name: name}
		if err := decodeJSONC(fsys, "presets/"+e.Name(), p); err != nil {
			return nil, fmt.Errorf("preset %s: %w", name, err)
		}
		for _, bn := range p.Blocks {
			if c.Blocks[bn] == nil {
				return nil, fmt.Errorf("preset %s: unknown block %q", name, bn)
			}
		}
		c.Presets[name] = p
	}
	return c, nil
}

func loadBlock(fsys fs.FS, name string) (*Block, error) {
	dir := "blocks/" + name
	var meta struct {
		Description string          `json:"description"`
		Base        bool            `json:"base"`
		Requires    []string        `json:"requires"`
		Params      json.RawMessage `json:"params"`
	}
	if err := decodeJSONC(fsys, dir+"/block.jsonc", &meta); err != nil {
		return nil, err
	}
	b := &Block{Name: name, Description: meta.Description, Base: meta.Base, Requires: meta.Requires}

	if len(meta.Params) > 0 {
		var params map[string]struct {
			Description string  `json:"description"`
			Default     *string `json:"default"`
		}
		if err := json.Unmarshal(meta.Params, &params); err != nil {
			return nil, fmt.Errorf("block.jsonc: params: %w", err)
		}
		// Keep the file's order: it is the order the wizard asks in.
		keys, err := objectKeys(meta.Params)
		if err != nil {
			return nil, fmt.Errorf("block.jsonc: params: %w", err)
		}
		for _, k := range keys {
			p := params[k]
			b.Params = append(b.Params, Param{Name: k, Description: p.Description, Default: p.Default})
		}
	}

	var err error
	if b.containerfile, err = loadTemplate(fsys, dir, "Containerfile"); err != nil {
		return nil, err
	}
	if b.devcontainer, err = loadTemplate(fsys, dir, "devcontainer.jsonc"); err != nil {
		return nil, err
	}
	if b.postCreate, err = loadTemplate(fsys, dir, "post-create.sh"); err != nil {
		return nil, err
	}
	if b.Base && b.containerfile == nil {
		return nil, fmt.Errorf("a base block needs a Containerfile")
	}
	return b, nil
}

// loadTemplate returns nil when the block has no such fragment.
func loadTemplate(fsys fs.FS, dir, file string) (*template.Template, error) {
	src, err := fs.ReadFile(fsys, path.Join(dir, file))
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := template.New(file).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, err
	}
	return t, nil
}

func decodeJSONC(fsys fs.FS, name string, v any) error {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	stripped, err := jsonc.Strip(src)
	if err != nil {
		return fmt.Errorf("%s: %w", path.Base(name), err)
	}
	dec := json.NewDecoder(bytes.NewReader(stripped))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path.Base(name), err)
	}
	return nil
}

// objectKeys returns the keys of a JSON object in document order.
func objectKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// BlockNames returns all block names, sorted.
func (c *Catalog) BlockNames() []string {
	return sortedKeys(c.Blocks)
}

// PresetNames returns all preset names, sorted.
func (c *Catalog) PresetNames() []string {
	return sortedKeys(c.Presets)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
