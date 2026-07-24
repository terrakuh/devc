package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// varPattern matches a ${...} reference. The body is parsed by expand.
var varPattern = regexp.MustCompile(`\$\{[^}]*\}`)

// SubstContext supplies the values used to expand ${...} references.
type SubstContext struct {
	LocalWorkspaceFolder     string
	ContainerWorkspaceFolder string
	DevcontainerID           string
	// LookupEnv defaults to os.LookupEnv; overridable for tests.
	LookupEnv func(string) (string, bool)
}

// Substitute expands ${...} variables in every string field of the Loaded
// Raw config, in place. containerWorkspaceFolder may itself reference
// ${localWorkspaceFolderBasename}, so expansion runs in up to three passes;
// any ${...} remaining afterward (other than the deferred ${containerEnv:...},
// which is resolved at exec time) is an error.
func Substitute(l *Loaded, id string) error {
	workFolder := l.Raw.WorkspaceFolder
	lookup := l.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	ctx := SubstContext{
		LocalWorkspaceFolder:     l.LocalWorkspaceFolder,
		ContainerWorkspaceFolder: workFolder,
		DevcontainerID:           id,
		LookupEnv:                lookup,
	}

	// Resolve containerWorkspaceFolder first (it feeds other fields).
	for pass := 0; pass < 3; pass++ {
		next, _, err := ctx.expand(ctx.ContainerWorkspaceFolder)
		if err != nil {
			return err
		}
		if next == ctx.ContainerWorkspaceFolder {
			break
		}
		ctx.ContainerWorkspaceFolder = next
	}
	l.Raw.WorkspaceFolder = ctx.ContainerWorkspaceFolder

	v := reflect.ValueOf(&l.Raw).Elem()
	for pass := 0; pass < 3; pass++ {
		changed, err := substituteValue(v, &ctx)
		if err != nil {
			return err
		}
		if !changed {
			break
		}
	}

	// Any leftover ${...} that is not a deferred containerEnv reference is an
	// error; scan the whole struct once more.
	return checkResidual(reflect.ValueOf(&l.Raw).Elem())
}

// substituteValue walks a reflect.Value, expanding every settable string it
// finds. It returns whether any value changed this pass. Deferred
// ${containerEnv:...} references are left intact.
func substituteValue(v reflect.Value, ctx *SubstContext) (bool, error) {
	changed := false
	switch v.Kind() {
	case reflect.String:
		if !v.CanSet() {
			return false, nil
		}
		out, deferred, err := ctx.expand(v.String())
		if err != nil {
			return false, err
		}
		_ = deferred
		if out != v.String() {
			v.SetString(out)
			changed = true
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			c, err := substituteValue(v.Elem(), ctx)
			changed = changed || c
			if err != nil {
				return changed, err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			c, err := substituteValue(v.Field(i), ctx)
			changed = changed || c
			if err != nil {
				return changed, err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			c, err := substituteValue(v.Index(i), ctx)
			changed = changed || c
			if err != nil {
				return changed, err
			}
		}
	case reflect.Map:
		// Map values are not addressable, so recurse into an addressable copy
		// and write it back. This reaches nested values of any kind, notably
		// the object (named) form of lifecycle commands, map[string]Command,
		// whose Shell/Argv strings would otherwise never be expanded.
		iter := v.MapRange()
		for iter.Next() {
			val := iter.Value()
			cp := reflect.New(val.Type()).Elem()
			cp.Set(val)
			c, err := substituteValue(cp, ctx)
			if err != nil {
				return changed, err
			}
			if c {
				v.SetMapIndex(iter.Key(), cp)
				changed = true
			}
		}
	}
	return changed, nil
}

// expand replaces every ${...} in s. It returns the result and whether any
// deferred (${containerEnv:...}) reference was left in place.
func (ctx *SubstContext) expand(s string) (string, bool, error) {
	if !strings.Contains(s, "${") {
		return s, false, nil
	}
	var expandErr error
	deferred := false
	out := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		body := match[2 : len(match)-1] // strip ${ and }
		repl, isDeferred, err := ctx.resolveVar(body)
		if err != nil {
			expandErr = err
			return match
		}
		if isDeferred {
			deferred = true
			return match
		}
		return repl
	})
	if expandErr != nil {
		return s, false, expandErr
	}
	return out, deferred, nil
}

func (ctx *SubstContext) resolveVar(body string) (repl string, deferred bool, err error) {
	switch {
	case body == "localWorkspaceFolder":
		return ctx.LocalWorkspaceFolder, false, nil
	case body == "localWorkspaceFolderBasename":
		return filepath.Base(ctx.LocalWorkspaceFolder), false, nil
	case body == "containerWorkspaceFolder":
		return ctx.ContainerWorkspaceFolder, false, nil
	case body == "containerWorkspaceFolderBasename":
		return filepath.Base(ctx.ContainerWorkspaceFolder), false, nil
	case body == "devcontainerId":
		return ctx.DevcontainerID, false, nil
	case strings.HasPrefix(body, "localEnv:"):
		name, def, hasDef := splitEnvRef(body[len("localEnv:"):])
		lookup := ctx.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		if v, ok := lookup(name); ok {
			return v, false, nil
		}
		if hasDef {
			return def, false, nil
		}
		return "", false, nil
	case strings.HasPrefix(body, "containerEnv:"):
		// Resolved inside the container at exec time; leave intact.
		return "", true, nil
	default:
		return "", false, fmt.Errorf("unknown variable ${%s}", body)
	}
}

// splitEnvRef parses "NAME" or "NAME:default" from a localEnv/containerEnv body.
func splitEnvRef(s string) (name, def string, hasDefault bool) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// checkResidual returns an error if any string still contains a ${...} that is
// not a deferred ${containerEnv:...}.
func checkResidual(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		for _, m := range varPattern.FindAllString(v.String(), -1) {
			body := m[2 : len(m)-1]
			if !strings.HasPrefix(body, "containerEnv:") {
				return fmt.Errorf("unresolved variable %s", m)
			}
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return checkResidual(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			if err := checkResidual(v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := checkResidual(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := checkResidual(iter.Value()); err != nil {
				return err
			}
		}
	}
	return nil
}
