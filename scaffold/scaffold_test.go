package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/terrakuh/devc/config"
)

// testCatalog builds a catalog from block name -> file -> content.
func testCatalog(t *testing.T, blocks map[string]map[string]string) *Catalog {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, files := range blocks {
		for file, content := range files {
			fsys["blocks/"+name+"/"+file] = &fstest.MapFile{Data: []byte(content)}
		}
	}
	c, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

var baseBlock = map[string]string{
	"block.jsonc":   `{"description": "base", "base": true}`,
	"Containerfile": "FROM scratch\n",
}

func render(t *testing.T, c *Catalog, names []string, params map[string]map[string]string) map[string]string {
	t.Helper()
	sel, err := c.Resolve(names, params)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	files, err := sel.Render("test")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Name] = string(f.Data)
	}
	return out
}

// Every builtin block must render into files devc itself accepts.
func TestBuiltinBlocksRender(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	for _, name := range c.BlockNames() {
		t.Run(name, func(t *testing.T) {
			blocks, err := c.Expand([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]map[string]string{}
			for _, b := range blocks {
				for _, p := range b.Params {
					if p.Default == nil {
						if params[b.Name] == nil {
							params[b.Name] = map[string]string{}
						}
						params[b.Name][p.Name] = "example.com"
					}
				}
			}
			checkFiles(t, render(t, c, []string{name}, params))
		})
	}
	for _, name := range c.PresetNames() {
		t.Run("preset/"+name, func(t *testing.T) {
			p := c.Presets[name]
			checkFiles(t, render(t, c, p.Blocks, p.Params))
		})
	}
}

func checkFiles(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	dc := filepath.Join(dir, ".devcontainer")
	if err := os.Mkdir(dc, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dc, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := config.LoadSpec(dir, ""); err != nil {
		t.Fatalf("devc rejects the generated devcontainer.json: %v\n%s", err, files["devcontainer.json"])
	}
	if !strings.Contains(files["Containerfile"], "\nFROM ") {
		t.Fatalf("Containerfile has no FROM:\n%s", files["Containerfile"])
	}
	if script, ok := files["post-create.sh"]; ok {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("post-create.sh: %v\n%s\n%s", err, out, script)
		}
	}
}

func TestExpandOrdersByRequires(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base": baseBlock,
		"a":    {"block.jsonc": `{"requires": ["b"]}`},
		"b":    {"block.jsonc": `{"requires": ["base"]}`},
		"c":    {"block.jsonc": `{"requires": ["base"]}`},
	})
	blocks, err := c.Expand([]string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range blocks {
		got = append(got, b.Name)
	}
	if want := "base c b a"; strings.Join(got, " ") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestExpandErrors(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base":  baseBlock,
		"base2": baseBlock,
		"x":     {"block.jsonc": `{"requires": ["y"]}`},
		"y":     {"block.jsonc": `{"requires": ["x"]}`},
		"plain": {"block.jsonc": `{}`},
	})
	cases := map[string]struct {
		names []string
		want  string
	}{
		"unknown":   {[]string{"nope"}, `unknown block "nope"`},
		"cycle":     {[]string{"base", "x"}, "x -> y -> x"},
		"no base":   {[]string{"plain"}, "no base block selected (one of: base, base2)"},
		"two bases": {[]string{"base", "base2"}, "only one base block allowed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.Expand(tc.names)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveParams(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base": baseBlock,
		"p": {"block.jsonc": `{
			"requires": ["base"],
			"params": {
				"host": {"description": "required"},
				"port": {"default": "22"},
				"key": {"default": ""}
			}
		}`},
	})
	if got := c.Blocks["p"].Params; got[0].Name != "host" || got[1].Name != "port" || got[2].Name != "key" {
		t.Fatalf("params lost their file order: %+v", got)
	}

	sel, err := c.Resolve([]string{"p"}, map[string]map[string]string{"p": {"host": "h"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sel.Params["p"]; got["host"] != "h" || got["port"] != "22" || got["key"] != "" {
		t.Fatalf("params = %v", got)
	}

	cases := map[string]struct {
		params map[string]map[string]string
		want   string
	}{
		"missing":      {nil, "missing parameters: p.host"},
		"empty":        {map[string]map[string]string{"p": {"host": ""}}, "missing parameters: p.host"},
		"unknown":      {map[string]map[string]string{"p": {"host": "h", "nope": "x"}}, `block p has no parameter "nope"`},
		"not selected": {map[string]map[string]string{"q": {"x": "y"}}, "block q, which is not selected"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.Resolve([]string{"p"}, tc.params)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRenderMergesDevcontainer(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base": baseBlock,
		"a": {
			"block.jsonc": `{"requires": ["base"]}`,
			"devcontainer.jsonc": `{
				// a's env
				"containerEnv": {"A": "1"},
				"runArgs": [
					// both need it
					"--network=host",
					"--a", // trailing
				],
				"init": true
			}`,
		},
		"b": {
			"block.jsonc": `{"requires": ["base"], "params": {"v": {"default": "say \"hi\""}}}`,
			"devcontainer.jsonc": `{
				// b's env
				"containerEnv": {"B": "{{.v}}"},
				"runArgs": [
					// both need it
					"--network=host"
				],
				"init": true
			}`,
		},
	})
	got := render(t, c, []string{"a", "b"}, nil)["devcontainer.json"]
	want := "// Created by `devc init` (blocks: base, a, b). Edit freely.\n" + `{
	"name": "test",
	"containerEnv": {
		// a's env
		"A": "1",
		// b's env
		"B": "say \"hi\""
	},
	"runArgs": [
		// both need it
		"--network=host",
		"--a" // trailing
	],
	"init": true
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderConflict(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base": baseBlock,
		"a":    {"block.jsonc": `{"requires": ["base"]}`, "devcontainer.jsonc": `{"build": {"target": "dev"}}`},
		"b":    {"block.jsonc": `{"requires": ["base"]}`, "devcontainer.jsonc": `{"build": {"target": "prod"}}`},
	})
	sel, err := c.Resolve([]string{"a", "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sel.Render("test")
	want := `devcontainer.json "build.target": a sets "dev" but b sets "prod"`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

func TestRenderSections(t *testing.T) {
	c := testCatalog(t, map[string]map[string]string{
		"base": baseBlock,
		"a": {
			"block.jsonc":    `{"description": "the a", "requires": ["base"], "params": {"v": {"default": "x"}}}`,
			"Containerfile":  "RUN echo {{.v}}\n\n\n",
			"post-create.sh": "echo a\n",
		},
		"b": {"block.jsonc": `{"requires": ["base"]}`},
	})
	files := render(t, c, []string{"a", "b"}, nil)

	if !strings.Contains(files["Containerfile"], "\nFROM scratch\n\n"+rule+"# a: the a\n"+rule+"RUN echo x\n") {
		t.Fatalf("Containerfile:\n%s", files["Containerfile"])
	}
	if !strings.HasSuffix(files["post-create.sh"], "set -euo pipefail\ncd \"$(dirname \"$0\")/..\"\n\n"+rule+"# a: the a\n"+rule+"echo a\n") {
		t.Fatalf("post-create.sh:\n%s", files["post-create.sh"])
	}
	if !strings.Contains(files["devcontainer.json"], `"postCreateCommand": ".devcontainer/post-create.sh"`) {
		t.Fatalf("devcontainer.json lacks postCreateCommand:\n%s", files["devcontainer.json"])
	}

	if _, ok := render(t, c, []string{"b"}, nil)["post-create.sh"]; ok {
		t.Fatal("post-create.sh written although no block has one")
	}
}

func TestParseTreeErrors(t *testing.T) {
	for _, src := range []string{
		`{"a": 1 "b": 2}`,
		`{"a" 1}`,
		`{"a": nope}`,
		`{"a": "x`,
		`{"a": 1`,
		`{} {}`,
		`{/* open`,
	} {
		if _, err := parseTree(src, "t"); err == nil {
			t.Errorf("parseTree(%q) accepted invalid input", src)
		}
	}
}
