// Package config loads and resolves devcontainer.json into a semantic Spec.
//
// Load performs discovery, JSONC stripping, decoding into Raw, variable
// substitution, and finally Resolve into a Spec. Each stage is exported so
// callers (and tests) can stop early.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/terrakuh/devc/jsonc"
)

// Loaded is the result of reading a devcontainer.json off disk: the decoded
// Raw plus the paths needed for substitution and relative-path resolution.
type Loaded struct {
	// ConfigPath is the absolute path of the devcontainer.json that was read.
	ConfigPath string
	// LocalWorkspaceFolder is the absolute project folder: the parent of a
	// .devcontainer/ directory, or the config file's own directory otherwise.
	LocalWorkspaceFolder string
	// Raw is the decoded configuration before variable substitution.
	Raw Raw
	// Warnings collects non-fatal diagnostics (unknown keys, etc.).
	Warnings []string

	// lookupEnv overrides os.LookupEnv during Substitute; nil means use the
	// process environment. Set only in tests.
	lookupEnv func(string) (string, bool)
}

// Discover resolves a user-supplied path (a project directory or a config file)
// to the devcontainer.json that applies, following the standard search order.
// If configOverride is non-empty it is used directly.
func Discover(path, configOverride string) (configPath, localFolder string, err error) {
	if configOverride != "" {
		abs, err := filepath.Abs(configOverride)
		if err != nil {
			return "", "", err
		}
		return abs, localWorkspaceFolder(abs), nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("path %s: %w", abs, err)
	}
	if !info.IsDir() {
		// A file was named directly.
		return abs, localWorkspaceFolder(abs), nil
	}

	candidates := []string{
		filepath.Join(abs, ".devcontainer", "devcontainer.json"),
		filepath.Join(abs, ".devcontainer.json"),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, abs, nil
		}
	}

	// .devcontainer/<subfolder>/devcontainer.json
	nested, err := filepath.Glob(filepath.Join(abs, ".devcontainer", "*", "devcontainer.json"))
	if err != nil {
		return "", "", err
	}
	switch len(nested) {
	case 0:
		return "", "", fmt.Errorf("no devcontainer.json found under %s (looked for .devcontainer/devcontainer.json, .devcontainer.json, .devcontainer/*/devcontainer.json)", abs)
	case 1:
		return nested[0], abs, nil
	default:
		sort.Strings(nested)
		return "", "", fmt.Errorf("multiple devcontainer.json found under %s; pass --config to choose:\n  %s",
			abs, strings.Join(nested, "\n  "))
	}
}

// localWorkspaceFolder derives the project folder from a config file path: the
// grandparent when the file sits in a .devcontainer/ directory (or a nested
// subfolder of it), else the file's own directory.
func localWorkspaceFolder(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(dir)
	if base == ".devcontainer" {
		return filepath.Dir(dir)
	}
	// Nested form: .devcontainer/<name>/devcontainer.json
	if filepath.Base(filepath.Dir(dir)) == ".devcontainer" {
		return filepath.Dir(filepath.Dir(dir))
	}
	// .devcontainer.json directly in the project root.
	return dir
}

// Load discovers, reads, strips, and decodes the devcontainer.json for path,
// returning the Raw config and the folder context. It does not apply variable
// substitution or resolve to a Spec; call Substitute then Resolve for that.
func Load(path, configOverride string) (*Loaded, error) {
	configPath, localFolder, err := Discover(path, configOverride)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(configPath) //nolint:gosec // path is user-provided by design
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	stripped, err := jsonc.Strip(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	var raw Raw
	dec := json.NewDecoder(strings.NewReader(string(stripped)))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, decorateJSONError(err, stripped))
	}

	l := &Loaded{
		ConfigPath:           configPath,
		LocalWorkspaceFolder: localFolder,
		Raw:                  raw,
	}
	for k := range raw.Extra {
		l.Warnings = append(l.Warnings, fmt.Sprintf("unknown property %q ignored", k))
	}
	sort.Strings(l.Warnings)
	return l, nil
}

// LoadSpec is the full pipeline: discover + read + strip + decode, apply
// variable substitution, and resolve to a validated Spec. Warnings from Load
// are returned alongside the Spec.
func LoadSpec(path, configOverride string) (*Spec, []string, error) {
	l, err := Load(path, configOverride)
	if err != nil {
		return nil, nil, err
	}
	// The workspace id feeds ${devcontainerId}; compute it from the same inputs
	// Resolve will use so the two agree.
	name := l.Raw.Name
	if name == "" {
		name = filepath.Base(l.LocalWorkspaceFolder)
	}
	id := WorkspaceID(name, l.LocalWorkspaceFolder)
	if err := Substitute(l, id); err != nil {
		return nil, l.Warnings, err
	}
	spec, err := Resolve(l)
	if err != nil {
		return nil, l.Warnings, err
	}
	return spec, l.Warnings, nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// decorateJSONError adds line/column context to a json.SyntaxError. Because
// JSONC stripping preserves offsets, the offset still maps to the original
// source layout.
func decorateJSONError(err error, src []byte) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		line, col := 1, 1
		for i := int64(0); i < se.Offset && int(i) < len(src); i++ {
			if src[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
		}
		return fmt.Errorf("JSON syntax error at line %d col %d: %w", line, col, err)
	}
	return err
}
