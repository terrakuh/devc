package scaffold

import (
	"fmt"
	"sort"
	"strings"
)

// Selection is a resolved set of blocks, ready to render.
type Selection struct {
	// Blocks in dependency order; the base block comes first.
	Blocks []*Block
	// Params holds every param of every block, block name -> param -> value.
	Params map[string]map[string]string
}

// Expand adds the blocks that names require and orders the result so every
// block comes after the blocks it requires, with the base block first.
func (c *Catalog) Expand(names []string) ([]*Block, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var order []*Block
	var visit func(name string, chain []string) error
	visit = func(name string, chain []string) error {
		b := c.Blocks[name]
		if b == nil {
			return fmt.Errorf("unknown block %q", name)
		}
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("blocks require each other: %s", strings.Join(append(chain, name), " -> "))
		}
		state[name] = visiting
		for _, r := range b.Requires {
			if err := visit(r, append(chain, name)); err != nil {
				return err
			}
		}
		state[name] = done
		order = append(order, b)
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}

	var base []*Block
	rest := order[:0:0]
	for _, b := range order {
		if b.Base {
			base = append(base, b)
		} else {
			rest = append(rest, b)
		}
	}
	switch len(base) {
	case 0:
		return nil, fmt.Errorf("no base block selected (one of: %s)", strings.Join(c.baseNames(), ", "))
	case 1:
		return append(base, rest...), nil
	default:
		return nil, fmt.Errorf("only one base block allowed, got %s and %s", base[0].Name, base[1].Name)
	}
}

func (c *Catalog) baseNames() []string {
	var out []string
	for _, n := range c.BlockNames() {
		if c.Blocks[n].Base {
			out = append(out, n)
		}
	}
	return out
}

// Resolve expands names and fills every param from params or its default.
// params is block name -> param -> value.
func (c *Catalog) Resolve(names []string, params map[string]map[string]string) (*Selection, error) {
	blocks, err := c.Expand(names)
	if err != nil {
		return nil, err
	}
	selected := map[string]*Block{}
	for _, b := range blocks {
		selected[b.Name] = b
	}
	for _, bn := range sortedKeys(params) {
		b := selected[bn]
		if b == nil {
			return nil, fmt.Errorf("parameters given for block %s, which is not selected", bn)
		}
		for _, pn := range sortedKeys(params[bn]) {
			if b.param(pn) == nil {
				return nil, fmt.Errorf("block %s has no parameter %q", bn, pn)
			}
		}
	}

	sel := &Selection{Blocks: blocks, Params: map[string]map[string]string{}}
	var missing []string
	for _, b := range blocks {
		vals := map[string]string{}
		for _, p := range b.Params {
			v, ok := params[b.Name][p.Name]
			if !ok && p.Default != nil {
				v, ok = *p.Default, true
			}
			if !ok || (v == "" && p.Default == nil) {
				missing = append(missing, b.Name+"."+p.Name)
				continue
			}
			vals[p.Name] = v
		}
		sel.Params[b.Name] = vals
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing parameters: %s", strings.Join(missing, ", "))
	}
	return sel, nil
}

func (b *Block) param(name string) *Param {
	for i := range b.Params {
		if b.Params[i].Name == name {
			return &b.Params[i]
		}
	}
	return nil
}
