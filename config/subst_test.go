package config

import "testing"

func TestSubstitute(t *testing.T) {
	l := &Loaded{
		LocalWorkspaceFolder: "/home/me/proj",
		Raw: Raw{
			Image:           "img:${localEnv:TAG:latest}",
			WorkspaceFolder: "/src/${localWorkspaceFolderBasename}",
			RemoteEnv: map[string]string{
				"ROOT": "${containerWorkspaceFolder}",
				"HOST": "${localEnv:HOME}",
				"MISS": "${localEnv:DEFINITELY_UNSET}",
			},
			RunArgs: []string{"--name", "${devcontainerId}"},
			ContainerEnv: map[string]string{
				"LATER": "${containerEnv:PATH}", // deferred, must survive
			},
		},
	}
	l.LookupEnvForTest(map[string]string{"HOME": "/home/me"})
	if err := Substitute(l, "proj-abc123"); err != nil {
		t.Fatal(err)
	}
	if l.Raw.WorkspaceFolder != "/src/proj" {
		t.Fatalf("workspaceFolder = %s", l.Raw.WorkspaceFolder)
	}
	if l.Raw.Image != "img:latest" {
		t.Fatalf("image = %s", l.Raw.Image)
	}
	if l.Raw.RemoteEnv["ROOT"] != "/src/proj" {
		t.Fatalf("ROOT = %s (containerWorkspaceFolder should have expanded)", l.Raw.RemoteEnv["ROOT"])
	}
	if l.Raw.RemoteEnv["HOST"] != "/home/me" {
		t.Fatalf("HOST = %s", l.Raw.RemoteEnv["HOST"])
	}
	if l.Raw.RemoteEnv["MISS"] != "" {
		t.Fatalf("MISS = %q (unset localEnv should be empty)", l.Raw.RemoteEnv["MISS"])
	}
	if l.Raw.RunArgs[1] != "proj-abc123" {
		t.Fatalf("devcontainerId = %s", l.Raw.RunArgs[1])
	}
	if l.Raw.ContainerEnv["LATER"] != "${containerEnv:PATH}" {
		t.Fatalf("deferred containerEnv was expanded: %s", l.Raw.ContainerEnv["LATER"])
	}
}

func TestSubstituteUnknownVar(t *testing.T) {
	l := &Loaded{LocalWorkspaceFolder: "/x", Raw: Raw{Image: "${bogusVariable}"}}
	if err := Substitute(l, "id"); err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

// TestSubstituteNamedCommand guards against the object (named) form of a
// lifecycle command being skipped by substitution. Its values are a
// map[string]Command, and a map case that only handled string values left the
// ${...} references intact - which then tripped checkResidual and failed the
// whole load with a spurious "unresolved variable" error.
func TestSubstituteNamedCommand(t *testing.T) {
	l := &Loaded{
		LocalWorkspaceFolder: "/home/me/proj",
		Raw: Raw{
			Image:           "img",
			WorkspaceFolder: "/src",
			PostCreateCommand: Command{
				Kind: CommandNamed,
				Named: map[string]Command{
					"install": {Kind: CommandShell, Shell: "make -C ${containerWorkspaceFolder}"},
					"tag":     {Kind: CommandArgv, Argv: []string{"echo", "${devcontainerId}"}},
					"defer":   {Kind: CommandShell, Shell: "echo ${containerEnv:PATH}"},
				},
			},
		},
	}
	if err := Substitute(l, "proj-abc123"); err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	named := l.Raw.PostCreateCommand.Named
	if got := named["install"].Shell; got != "make -C /src" {
		t.Errorf("install.Shell = %q, want %q", got, "make -C /src")
	}
	if got := named["tag"].Argv; len(got) != 2 || got[1] != "proj-abc123" {
		t.Errorf("tag.Argv = %v, want [echo proj-abc123]", got)
	}
	// Deferred containerEnv must survive untouched inside a named command too.
	if got := named["defer"].Shell; got != "echo ${containerEnv:PATH}" {
		t.Errorf("defer.Shell = %q, deferred containerEnv should be left intact", got)
	}
}
