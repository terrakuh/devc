package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/container"
	"github.com/terrakuh/devc/runtime"
)

// Shell completion: `devc completion <shell>` prints a small script that calls
// back into the hidden `devc __complete <word...>` for every TAB, so all the
// logic (and the flag list) lives here, next to the commands it describes.

// completionCommands are the user-facing commands, in the order `devc <TAB>`
// offers them. run is nil for commands without a flag set.
var completionCommands = []struct {
	name string
	run  func([]string) error
}{
	{"up", runUp},
	{"down", runDown},
	{"stop", runStop},
	{"restart", runRestart},
	{"status", runStatus},
	{"ps", runPs},
	{"list", runList},
	{"logs", runLogs},
	{"exec", runExec},
	{"ssh", runSSH},
	{"code", runCode},
	{"ssh-config", runSSHConfig},
	{"keys", runKeys},
	{"doctor", runDoctor},
	{"config", runConfig},
	{"completion", nil},
	{"version", nil},
	{"help", nil},
}

// serviceCommands take compose service names as positional arguments.
var serviceCommands = map[string]bool{
	"up": true, "down": true, "stop": true, "restart": true, "logs": true, "ps": true,
}

var completionShells = []string{"bash", "zsh", "fish"}

// capturedFlags, when non-nil, receives every flag set newFlagSet builds. The
// completer sets it and runs a command with -h: each command parses its flags
// before doing anything else, so it returns flag.ErrHelp having only declared
// them, and the completer reads the real flag set instead of a copy of it.
var capturedFlags *[]*flag.FlagSet

// newFlagSet is how commands create their flag set, so completion sees it.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if capturedFlags != nil {
		fs.SetOutput(io.Discard)
		*capturedFlags = append(*capturedFlags, fs)
	}
	return fs
}

// commandFlags returns the flag set the named command declares, or nil.
func commandFlags(name string) *flag.FlagSet {
	for _, c := range completionCommands {
		if c.name != name || c.run == nil {
			continue
		}
		var got []*flag.FlagSet
		capturedFlags = &got
		err := c.run([]string{"-h"})
		capturedFlags = nil
		if !errors.Is(err, flag.ErrHelp) || len(got) == 0 {
			return nil
		}
		return got[0]
	}
	return nil
}

// directiveFiles, printed alone, hands the word back to the shell's own file
// completion.
const directiveFiles = ":files"

// runCompletion implements `devc completion <shell>`.
func runCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: devc completion %s", strings.Join(completionShells, "|"))
	}
	script, ok := map[string]string{
		"bash": bashCompletion,
		"zsh":  zshCompletion,
		"fish": fishCompletion,
	}[args[0]]
	if !ok {
		return fmt.Errorf("unsupported shell %q (want %s)", args[0], strings.Join(completionShells, ", "))
	}
	_, err := io.WriteString(os.Stdout, script)
	return err
}

// runComplete implements the hidden `devc __complete <word...>`: the words after
// "devc" up to and including the one being completed (possibly empty). It
// prints one candidate per line as "value" or "value\tdescription".
func runComplete(words []string) error {
	for _, c := range complete(context.Background(), words) {
		fmt.Println(c)
	}
	return nil
}

func complete(ctx context.Context, words []string) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	cur := words[len(words)-1]
	if len(words) == 1 {
		var out []string
		for _, c := range completionCommands {
			out = append(out, c.name)
		}
		return filterPrefix(out, cur)
	}

	cmd, args := words[0], words[1:len(words)-1]
	if cmd == "completion" {
		if len(args) == 0 {
			return filterPrefix(completionShells, cur)
		}
		return nil
	}
	fs := commandFlags(cmd)
	if fs == nil {
		return nil
	}

	// Walk the words before cur the way flag.Parse would, to learn whether cur
	// is a flag value, a flag, or a positional argument.
	var pending *flag.Flag // flag whose separate value cur is
	positional := false
	for _, w := range args {
		if pending != nil {
			pending = nil
			continue
		}
		if w == "--" || !strings.HasPrefix(w, "-") || w == "-" {
			positional = true
			break
		}
		name := strings.TrimLeft(w, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) {
			pending = f
		}
	}
	if pending != nil {
		return completeFlagValue(ctx, fs, args, pending, "", cur)
	}

	if !positional && strings.HasPrefix(cur, "-") {
		if name, val, ok := strings.Cut(strings.TrimLeft(cur, "-"), "="); ok {
			f := fs.Lookup(name)
			if f == nil {
				return nil
			}
			return completeFlagValue(ctx, fs, args, f, cur[:len(cur)-len(val)], val)
		}
		return filterPrefix(flagCandidates(fs), cur)
	}

	switch {
	case serviceCommands[cmd]:
		return filterPrefix(services(ctx, fs, args), cur)
	case cmd == "exec":
		// The command to run inside the container; plain file completion.
		return []string{directiveFiles}
	}
	return nil
}

// flagCandidates lists fs's flags as "-x" (one letter) or "--name", with usage.
func flagCandidates(fs *flag.FlagSet) []string {
	var out []string
	fs.VisitAll(func(f *flag.Flag) {
		dash := "--"
		if len(f.Name) == 1 {
			dash = "-"
		}
		out = append(out, dash+f.Name+"\t"+f.Usage)
	})
	return out
}

// completeFlagValue completes val as the value of flag f. prefix is prepended to
// every candidate (the "--flag=" of the inline form).
func completeFlagValue(ctx context.Context, fs *flag.FlagSet, args []string, f *flag.Flag, prefix, val string) []string {
	var out []string
	switch f.Name {
	case "path", "config":
		return []string{directiveFiles}
	case "runtime":
		out = []string{"podman", "docker"}
	case "selinux":
		out = []string{"auto", "z", "Z", "none"}
	case "editor":
		out = []string{"codium", "code"}
	case "name", "n":
		out = workspaceNames(ctx, fs, args)
	case "service":
		out = services(ctx, fs, args)
	default:
		if isBoolFlag(f) {
			out = []string{"true", "false"}
		}
	}
	out = filterPrefix(out, val)
	for i := range out {
		out[i] = prefix + out[i]
	}
	return out
}

// parsedFlags parses the words already typed into a fresh copy of the command's
// flag set and returns the common flags they select (quietly: completion must
// never print warnings into the prompt).
func parsedFlags(fs *flag.FlagSet, args []string) *commonFlags {
	_ = fs.Parse(args)
	get := func(name string) string {
		if f := fs.Lookup(name); f != nil {
			return f.Value.String()
		}
		return ""
	}
	path := get("path")
	if path == "" {
		path = "."
	}
	return &commonFlags{
		path:       path,
		name:       get("name"),
		configFile: get("config"),
		runtime:    get("runtime"),
		composeCmd: get("compose-cmd"),
		quiet:      true,
	}
}

// workspaceNames lists every devc workspace name on the host.
func workspaceNames(ctx context.Context, fs *flag.FlagSet, args []string) []string {
	r, err := runtime.Detect(parsedFlags(fs, args).runtime)
	if err != nil {
		return nil
	}
	infos, err := listWorkspaces(ctx, r)
	if err != nil {
		return nil
	}
	var out []string
	for _, info := range infos {
		if name := info.Config.Labels[container.LabelName]; name != "" {
			out = append(out, name+"\t"+info.Config.Labels[container.LabelLocal])
		}
	}
	sort.Strings(out)
	return out
}

// services lists the compose services of the workspace the typed flags select.
func services(ctx context.Context, fs *flag.FlagSet, args []string) []string {
	e, err := setup(ctx, parsedFlags(fs, args))
	if err != nil || e.spec.Kind != config.KindCompose {
		return nil
	}
	comp, err := e.composeImpl(ctx)
	if err != nil {
		return nil
	}
	names, err := container.ComposeServiceNames(ctx, comp, e.spec, container.ProjectName(e.spec))
	if err != nil {
		return nil
	}
	return names
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// filterPrefix keeps the candidates whose value (before any tab) starts with p.
func filterPrefix(cands []string, p string) []string {
	var out []string
	for _, c := range cands {
		if v, _, _ := strings.Cut(c, "\t"); strings.HasPrefix(v, p) {
			out = append(out, c)
		}
	}
	return out
}

// bashCompletion needs no bash-completion package. Bash splits "--flag=value"
// into three words (COMP_WORDBREAKS has '='), so the words are rejoined before
// asking devc and the "--flag=" part is stripped back off the candidates.
const bashCompletion = `# devc bash completion. Load with:
#   source <(devc completion bash)
_devc() {
    local words=() i w
    for ((i = 1; i <= COMP_CWORD; i++)); do
        w=${COMP_WORDS[i]}
        if ((${#words[@]})) && [[ $w == = || ${COMP_WORDS[i-1]} == = ]]; then
            words[${#words[@]}-1]+=$w
        else
            words+=("$w")
        fi
    done

    local out
    out=$(devc __complete "${words[@]}" 2>/dev/null) || return
    local IFS=$'\n'
    local cands=($out)
    ((${#cands[@]})) || return
    if [[ ${cands[-1]} == :files ]]; then
        compopt -o default
        COMPREPLY=()
        return
    fi
    COMPREPLY=("${cands[@]%%$'\t'*}")
    local cur=${words[${#words[@]}-1]}
    if [[ $cur == *=* && $COMP_WORDBREAKS == *=* ]]; then
        COMPREPLY=("${COMPREPLY[@]#*=}")
    fi
}
complete -F _devc devc
`

const zshCompletion = `#compdef devc
# devc zsh completion. Load with:
#   source <(devc completion zsh)
# or save it as _devc in a directory on $fpath.
_devc() {
    local -a out cands
    local l
    out=("${(@f)$(devc __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}")
    if [[ $out[-1] == :files ]]; then
        compset -P '*='
        _files
        return
    fi
    for l in $out; do
        [[ -n $l ]] || continue
        if [[ $l == *$'\t'* ]]; then
            cands+=("${${l%%$'\t'*}//:/\\:}:${l#*$'\t'}")
        else
            cands+=("${l//:/\\:}")
        fi
    done
    _describe -t devc devc cands
}
if [[ $zsh_eval_context[-1] == loadautofunc ]]; then
    _devc "$@"
else
    compdef _devc devc
fi
`

const fishCompletion = `# devc fish completion. Load with:
#   devc completion fish | source
# or save it as ~/.config/fish/completions/devc.fish.
function __devc_complete
    set -l tokens (commandline -opc) (commandline -ct)
    set -e tokens[1]
    set -l out (devc __complete $tokens 2>/dev/null)
    if test "$out[-1]" = :files
        set -l cur (commandline -ct)
        set -l pre (string match -r -- '^-[^=]*=' $cur)
        for p in (__fish_complete_path (string replace -r -- '^-[^=]*=' '' $cur))
            echo $pre$p
        end
        return
    end
    printf '%s\n' $out
end
complete -c devc -f -a '(__devc_complete)'
`
