package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/terrakuh/devc/config"
	"github.com/terrakuh/devc/keys"
	"github.com/terrakuh/devc/runtime"
	"github.com/terrakuh/devc/sshconf"
	"github.com/terrakuh/devc/state"
)

// runKeys implements `devc keys [--rotate]`: show or rotate the workspace's SSH
// key material. Rotation invalidates the previous identity; the container must
// be re-provisioned (via `devc up`) afterward.
func runKeys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	rotate := fs.Bool("rotate", false, "generate fresh keys, discarding the old identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Keys only need the resolved spec (id + alias), not a runtime.
	spec, warns, err := loadSpecOnly(&cf)
	if err != nil {
		return err
	}
	printWarnings(cf.quiet, warns)

	dir, err := state.For(spec.ID)
	if err != nil {
		return err
	}
	alias := "devc." + spec.Name

	var set *keys.Set
	if *rotate {
		set, err = keys.Rotate(dir.Root, alias)
		if err != nil {
			return err
		}
		fmt.Printf("rotated keys for %q - run `devc up` to re-provision the container\n", spec.Name)
	} else {
		set, err = keys.Ensure(dir.Root, alias)
		if err != nil {
			return err
		}
	}

	pub, err := os.ReadFile(set.ClientPublic)
	if err != nil {
		return err
	}
	fmt.Printf("workspace:      %s\n", spec.Name)
	fmt.Printf("key dir:        %s\n", set.Dir)
	fmt.Printf("client pubkey:  %s", pub)
	return nil
}

// runSSHConfig implements `devc ssh-config [--print]`: regenerate (or preview)
// the workspace's ssh config block. Without a running container it still works,
// producing the block from the spec + existing keys.
func runSSHConfig(args []string) error {
	fs := flag.NewFlagSet("ssh-config", flag.ContinueOnError)
	var cf commonFlags
	cf.register(fs)
	printOnly := fs.Bool("print", false, "print the config instead of writing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec, warns, err := loadSpecOnly(&cf)
	if err != nil {
		return err
	}
	printWarnings(cf.quiet, warns)
	// Honor the same credential flag overrides `setup` applies, so the block this
	// command writes matches what `devc up` would write for the same invocation.
	if cf.forwardAgent.set {
		spec.Credentials.ForwardAgent = cf.forwardAgent.val
	}

	dir, err := state.For(spec.ID)
	if err != nil {
		return err
	}
	set, err := keys.Ensure(dir.Root, "devc."+spec.Name)
	if err != nil {
		return err
	}
	self, _ := os.Executable()

	var forwards []sshconf.Forward
	for _, p := range spec.Ports {
		forwards = append(forwards, sshconf.Forward{HostPort: p.HostPort, ContainerPort: p.ContainerPort})
	}
	ctlDir, err := ensureControlDir(spec.ID)
	if err != nil {
		return err
	}
	// Best-effort: bake the runtime path in when one is available now, so the
	// generated ProxyCommand does not depend on PATH. If detection fails here we
	// still emit a usable block (the ProxyCommand resolves the runtime at connect).
	var runtimePath string
	if r, derr := runtime.Detect(cf.runtime); derr == nil {
		runtimePath = runtimeBinPath(r)
	}
	block := sshconf.Render(sshconf.Params{
		Alias:          "devc." + spec.Name,
		User:           spec.RemoteUser,
		DevcBin:        self,
		ConfigPath:     spec.ConfigPath,
		RuntimePath:    runtimePath,
		WorkspaceName:  spec.Name,
		IdentityFile:   set.ClientPrivate,
		KnownHostsFile: set.KnownHosts,
		ControlDir:     ctlDir,
		Forwards:       forwards,
		ForwardAgent:   spec.Credentials.ForwardAgent,
	})
	content := sshconf.File(block)

	if *printOnly {
		fmt.Print(content)
		return nil
	}
	confPath := devcSSHConfigPath(spec.ID)
	if err := sshconf.WriteWorkspaceConfig(confPath, content); err != nil {
		return err
	}
	changed, err := sshconf.EnsureInclude(userSSHConfigPath(), devcSSHIncludeGlob())
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", confPath)
	if changed {
		fmt.Printf("added `Include %s` to %s\n", devcSSHIncludeGlob(), userSSHConfigPath())
	}
	return nil
}

// loadSpecOnly resolves the spec without selecting a runtime, for commands that
// only touch host-side state (keys, ssh-config).
func loadSpecOnly(cf *commonFlags) (*config.Spec, []string, error) {
	return config.LoadSpec(cf.path, cf.configFile)
}

func printWarnings(quiet bool, warns []string) {
	if quiet {
		return
	}
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}
