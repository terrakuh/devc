package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/terrakuh/devc/scaffold"
)

var errAborted = errors.New("aborted")

// initRequest is what the command line asked for.
type initRequest struct {
	preset  string
	with    []string
	without []string
	params  map[string]map[string]string // from --set
	name    string
}

// runInit implements `devc init`: it composes .devcontainer/ from blocks,
// interactively when neither --preset nor --with is given and stdin is a
// terminal.
func runInit(args []string) error {
	fs := newFlagSet("init")
	path := fs.String("path", ".", "project directory")
	preset := fs.String("preset", "", "start from this preset")
	with := fs.String("with", "", "comma-separated blocks to add")
	without := fs.String("without", "", "comma-separated blocks to drop from the preset")
	sets := paramFlag{}
	fs.Var(sets, "set", "block parameter as block.param=value (repeatable)")
	name := fs.String("name", "", "devcontainer.json name (default: the project folder's name)")
	list := fs.Bool("list", false, "list blocks and presets")
	printOnly := fs.Bool("print", false, "print the files instead of writing them")
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("init takes no arguments, got %q", fs.Args())
	}

	cat, err := loadCatalog(context.Background())
	if err != nil {
		return err
	}
	if *list {
		printCatalog(os.Stdout, cat)
		return nil
	}

	dir, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if !*printOnly && !*force {
		if err := checkExisting(dir); err != nil {
			return err
		}
	}
	req := initRequest{
		preset:  *preset,
		with:    splitList(*with),
		without: splitList(*without),
		params:  sets,
		name:    *name,
	}
	if req.name == "" {
		req.name = filepath.Base(dir)
	}

	var files []scaffold.File
	var command string // the wizard's choices as flags
	if req.preset == "" && len(req.with) == 0 && stdinIsTerminal() {
		w := &wizard{cat: cat, in: bufio.NewScanner(os.Stdin), out: os.Stdout}
		if files, command, err = w.run(req); err != nil {
			return err
		}
	} else {
		if files, err = renderRequest(cat, req); err != nil {
			return err
		}
	}

	if *printOnly {
		printFiles(os.Stdout, files)
		return nil
	}
	if err := writeFiles(dir, files); err != nil {
		return err
	}
	if command != "" {
		fmt.Printf("\nSame setup without questions:\n  %s\n", command)
	}
	fmt.Println("\nNext: devc up")
	return nil
}

func renderRequest(cat *scaffold.Catalog, req initRequest) ([]scaffold.File, error) {
	if req.preset == "" && len(req.with) == 0 {
		return nil, fmt.Errorf("nothing selected: use --preset or --with (see devc init --list)")
	}
	names, params, err := selectBlocks(cat, req)
	if err != nil {
		return nil, err
	}
	sel, err := cat.Resolve(names, params)
	if err != nil {
		return nil, err
	}
	return sel.Render(req.name)
}

// selectBlocks applies --preset, --with and --without, and lays --set over the
// preset's params.
func selectBlocks(cat *scaffold.Catalog, req initRequest) ([]string, map[string]map[string]string, error) {
	var names []string
	params := map[string]map[string]string{}
	if req.preset != "" {
		p := cat.Presets[req.preset]
		if p == nil {
			return nil, nil, fmt.Errorf("unknown preset %q (one of: %s)", req.preset, strings.Join(cat.PresetNames(), ", "))
		}
		names = slices.Clone(p.Blocks)
		addParams(params, p.Params)
	}
	addParams(params, req.params)
	for _, n := range req.with {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(req.without, n) })

	blocks, err := cat.Expand(names)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range req.without {
		if cat.Blocks[w] == nil {
			return nil, nil, fmt.Errorf("unknown block %q", w)
		}
		for _, b := range blocks {
			if slices.Contains(b.Requires, w) {
				return nil, nil, fmt.Errorf("cannot drop %s: %s requires it", w, b.Name)
			}
		}
	}
	return names, params, nil
}

func addParams(dst, src map[string]map[string]string) {
	for b, ps := range src {
		if dst[b] == nil {
			dst[b] = map[string]string{}
		}
		for k, v := range ps {
			dst[b][k] = v
		}
	}
}

// paramFlag collects --set block.param=value.
type paramFlag map[string]map[string]string

func (p paramFlag) String() string { return "" }

func (p paramFlag) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	block, param, ok2 := strings.Cut(key, ".")
	if !ok || !ok2 || block == "" || param == "" {
		return fmt.Errorf("want block.param=value, got %q", s)
	}
	if p[block] == nil {
		p[block] = map[string]string{}
	}
	p[block][param] = val
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func printCatalog(w io.Writer, cat *scaffold.Catalog) {
	fmt.Fprintln(w, "Presets:")
	for _, n := range cat.PresetNames() {
		p := cat.Presets[n]
		fmt.Fprintf(w, "  %-16s %s%s\n  %-16s blocks: %s\n", n, p.Description, sourceNote(p.Source), "", strings.Join(p.Blocks, ", "))
	}
	fmt.Fprintln(w, "\nBlocks (parameters are set with --set block.param=value):")
	for _, n := range cat.BlockNames() {
		b := cat.Blocks[n]
		fmt.Fprintf(w, "  %-16s %s%s\n", n, b.Description, sourceNote(b.Source))
		for _, p := range b.Params {
			def := "required"
			if p.Default != nil {
				def = "default " + strconv.Quote(*p.Default)
			}
			fmt.Fprintf(w, "  %-16s   %s.%s: %s (%s)\n", "", n, p.Name, p.Description, def)
		}
	}
}

func printFiles(w io.Writer, files []scaffold.File) {
	for i, f := range files {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "==> .devcontainer/%s <==\n%s", f.Name, f.Data)
	}
}

// generatedNames are the files init may write; it refuses to overwrite them
// without --force.
var generatedNames = []string{"devcontainer.json", "Containerfile", "post-create.sh"}

func checkExisting(dir string) error {
	var existing []string
	for _, n := range generatedNames {
		p := filepath.Join(dir, ".devcontainer", n)
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, ".devcontainer/"+n)
		}
	}
	if len(existing) > 0 {
		return fmt.Errorf("%s already exist(s); use --force to overwrite or --print to only show the result", strings.Join(existing, ", "))
	}
	return nil
}

func writeFiles(dir string, files []scaffold.File) error {
	dc := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dc, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		mode := os.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		p := filepath.Join(dc, f.Name)
		if err := os.WriteFile(p, f.Data, mode); err != nil {
			return err
		}
		// WriteFile keeps the mode of a file it overwrites.
		if err := os.Chmod(p, mode); err != nil {
			return err
		}
		fmt.Printf("wrote .devcontainer/%s\n", f.Name)
	}
	return nil
}

// wizard asks for preset, blocks, params and name on a terminal.
type wizard struct {
	cat *scaffold.Catalog
	in  *bufio.Scanner
	out io.Writer
}

func (w *wizard) ask(prompt string) (string, error) {
	fmt.Fprint(w.out, prompt)
	if !w.in.Scan() {
		fmt.Fprintln(w.out)
		if err := w.in.Err(); err != nil {
			return "", err
		}
		return "", errAborted
	}
	return strings.TrimSpace(w.in.Text()), nil
}

// run walks through the choices and returns the files plus the equivalent
// non-interactive command line.
func (w *wizard) run(req initRequest) ([]scaffold.File, string, error) {
	var selected []string
	params := map[string]map[string]string{}
	addParams(params, req.params)

	presets := w.cat.PresetNames()
	if len(presets) > 0 {
		fmt.Fprintln(w.out, "Presets:")
		for i, n := range presets {
			p := w.cat.Presets[n]
			fmt.Fprintf(w.out, "  %2d  %-16s %s%s\n", i+1, n, p.Description, sourceNote(p.Source))
		}
		for {
			a, err := w.ask("Preset (number or name, empty for none): ")
			if err != nil {
				return nil, "", err
			}
			if a == "" {
				break
			}
			if n := pick(a, presets); n != "" {
				p := w.cat.Presets[n]
				selected = slices.Clone(p.Blocks)
				addParams(params, p.Params)
				addParams(params, req.params)
				break
			}
			fmt.Fprintf(w.out, "  no preset %q\n", a)
		}
	}

	names := w.cat.BlockNames()
	for {
		blocks, expandErr := w.cat.Expand(selected)
		fmt.Fprintln(w.out, "\nBlocks (* chosen, + needed by a chosen one):")
		for i, n := range names {
			mark := " "
			if slices.Contains(selected, n) {
				mark = "*"
			} else if slices.ContainsFunc(blocks, func(b *scaffold.Block) bool { return b.Name == n }) {
				mark = "+"
			}
			b := w.cat.Blocks[n]
			fmt.Fprintf(w.out, "  %2d %s %-16s %s%s\n", i+1, mark, n, b.Description, sourceNote(b.Source))
		}
		if expandErr != nil && len(selected) > 0 {
			fmt.Fprintf(w.out, "  ! %v\n", expandErr)
		}
		a, err := w.ask("Toggle blocks (numbers or names; empty when done): ")
		if err != nil {
			return nil, "", err
		}
		if a == "" {
			if expandErr == nil {
				break
			}
			if len(selected) == 0 {
				fmt.Fprintln(w.out, "  choose at least one block")
			}
			continue
		}
		for _, f := range strings.Fields(strings.ReplaceAll(a, ",", " ")) {
			n := pick(f, names)
			switch {
			case n == "":
				fmt.Fprintf(w.out, "  no block %q\n", f)
			case slices.Contains(selected, n):
				selected = slices.DeleteFunc(selected, func(s string) bool { return s == n })
			default:
				selected = append(selected, n)
			}
		}
	}

	blocks, err := w.cat.Expand(selected)
	if err != nil {
		return nil, "", err
	}
	var changed []string // block.param=value differing from the default, for the command line
	for _, b := range blocks {
		if len(b.Params) > 0 {
			fmt.Fprintf(w.out, "\n%s:\n", b.Name)
		}
		for _, p := range b.Params {
			def, ok := params[b.Name][p.Name]
			if !ok && p.Default != nil {
				def = *p.Default
			}
			prompt := fmt.Sprintf("  %s - %s", p.Name, p.Description)
			if def != "" {
				prompt += " [" + def + "]"
			}
			var v string
			for {
				if v, err = w.ask(prompt + ": "); err != nil {
					return nil, "", err
				}
				if v == "" {
					v = def
				}
				if v != "" || p.Default != nil {
					break
				}
				fmt.Fprintln(w.out, "  required")
			}
			if params[b.Name] == nil {
				params[b.Name] = map[string]string{}
			}
			params[b.Name][p.Name] = v
			if p.Default == nil || v != *p.Default {
				changed = append(changed, b.Name+"."+p.Name+"="+v)
			}
		}
	}

	name, err := w.ask(fmt.Sprintf("\nName [%s]: ", req.name))
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		name = req.name
	}

	sel, err := w.cat.Resolve(selected, params)
	if err != nil {
		return nil, "", err
	}
	files, err := sel.Render(name)
	if err != nil {
		return nil, "", err
	}

	fileNames := make([]string, len(files))
	for i, f := range files {
		fileNames[i] = f.Name
	}
	for {
		a, err := w.ask(fmt.Sprintf("\nWrite %s to .devcontainer/? [Y/n/p=print] ", strings.Join(fileNames, ", ")))
		if err != nil {
			return nil, "", err
		}
		switch strings.ToLower(a) {
		case "", "y", "yes":
			command := []string{"devc init", "--with " + strings.Join(selected, ",")}
			for _, c := range changed {
				command = append(command, "--set "+shellQuote(c))
			}
			if name != req.name {
				command = append(command, "--name "+shellQuote(name))
			}
			return files, strings.Join(command, " "), nil
		case "n", "no":
			return nil, "", errAborted
		case "p", "print":
			fmt.Fprintln(w.out)
			printFiles(w.out, files)
		}
	}
}

// sourceNote marks blocks and presets that come from a store.
func sourceNote(source string) string {
	if source == scaffold.BuiltinSource {
		return ""
	}
	return " [" + source + "]"
}

// pick resolves a 1-based number or a name against options; "" if neither.
func pick(answer string, options []string) string {
	if i, err := strconv.Atoi(answer); err == nil {
		if i >= 1 && i <= len(options) {
			return options[i-1]
		}
		return ""
	}
	if slices.Contains(options, answer) {
		return answer
	}
	return ""
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@,+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
