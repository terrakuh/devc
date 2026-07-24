package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Raw mirrors the subset of devcontainer.json that devc understands. It is a
// faithful decode of the file (after JSONC stripping and variable
// substitution); the semantic Spec is derived from it by Resolve.
//
// Union-typed properties use the helper types below (StringOrArray, Command,
// PortList, MountList) so a single struct can decode every shape the spec
// allows. Unknown keys are captured in Extra for warning, not decoded.
type Raw struct {
	Name string `json:"name"`

	// Single-container: image OR build.
	Image string    `json:"image"`
	Build *RawBuild `json:"build"`

	// Compose.
	DockerComposeFile StringOrArray `json:"dockerComposeFile"`
	Service           string        `json:"service"`
	RunServices       []string      `json:"runServices"`

	WorkspaceFolder string `json:"workspaceFolder"`
	WorkspaceMount  string `json:"workspaceMount"`

	Mounts        MountList         `json:"mounts"`
	ContainerEnv  map[string]string `json:"containerEnv"`
	RemoteEnv     map[string]string `json:"remoteEnv"`
	ContainerUser string            `json:"containerUser"`
	RemoteUser    string            `json:"remoteUser"`

	ForwardPorts PortList `json:"forwardPorts"`
	AppPort      PortList `json:"appPort"`

	RunArgs         []string `json:"runArgs"`
	Init            *bool    `json:"init"`
	Privileged      *bool    `json:"privileged"`
	CapAdd          []string `json:"capAdd"`
	SecurityOpt     []string `json:"securityOpt"`
	OverrideCommand *bool    `json:"overrideCommand"`

	ShutdownAction string `json:"shutdownAction"`
	UserEnvProbe   string `json:"userEnvProbe"`
	WaitFor        string `json:"waitFor"`

	InitializeCommand    Command `json:"initializeCommand"`
	OnCreateCommand      Command `json:"onCreateCommand"`
	UpdateContentCommand Command `json:"updateContentCommand"`
	PostCreateCommand    Command `json:"postCreateCommand"`
	PostStartCommand     Command `json:"postStartCommand"`
	PostAttachCommand    Command `json:"postAttachCommand"`

	// Customizations is the devcontainer tool-config map. devc reads only its own
	// "devc" subkey and ignores the rest (e.g. "vscode").
	Customizations RawCustomizations `json:"customizations"`

	// Explicitly unsupported; decoded so Resolve can reject them clearly.
	Features            json.RawMessage `json:"features"`
	HostRequirements    json.RawMessage `json:"hostRequirements"`
	UpdateRemoteUserUID *bool           `json:"updateRemoteUserUID"`

	// Extra holds any key not mapped above, for a diagnostic warning. Populated
	// by UnmarshalJSON.
	Extra map[string]json.RawMessage `json:"-"`
}

// RawCustomizations holds the "customizations" object. Only the "devc" entry is
// decoded; other tools' entries are dropped.
type RawCustomizations struct {
	Devc *RawDevc `json:"devc"`
}

// RawDevc is the "customizations.devc" block: devc's own options.
type RawDevc struct {
	Credentials *RawCredentials `json:"credentials"`
}

// RawCredentials selects which host credentials are made available in the
// container. Pointers tell "unset" apart from "false". Room to add more
// mechanisms later (gpg, git credential helper, docker).
type RawCredentials struct {
	// ForwardAgent forwards the host ssh-agent; keys stay on the host and only a
	// proxy socket plus SSH_AUTH_SOCK exist in the container.
	ForwardAgent *bool `json:"forwardAgent"`
	// SyncGitConfig copies curated host git config (identity, signing) into the
	// container on `devc up`.
	SyncGitConfig *bool `json:"syncGitConfig"`
}

// RawBuild is the object form of the "build" property.
type RawBuild struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
	Target     string            `json:"target"`
	CacheFrom  StringOrArray     `json:"cacheFrom"`
	Options    []string          `json:"options"`
}

// knownKeys is the set of properties Raw maps explicitly; anything else lands
// in Extra. Kept in sync with the json tags above by TestKnownKeys.
var knownKeys = map[string]bool{
	"name": true, "image": true, "build": true, "dockerComposeFile": true,
	"service": true, "runServices": true, "workspaceFolder": true,
	"workspaceMount": true, "mounts": true, "containerEnv": true,
	"remoteEnv": true, "containerUser": true, "remoteUser": true,
	"forwardPorts": true, "appPort": true, "runArgs": true, "init": true,
	"privileged": true, "capAdd": true, "securityOpt": true,
	"overrideCommand": true, "shutdownAction": true, "userEnvProbe": true,
	"waitFor": true, "initializeCommand": true, "onCreateCommand": true,
	"updateContentCommand": true, "postCreateCommand": true,
	"postStartCommand": true, "postAttachCommand": true, "features": true,
	"hostRequirements": true, "updateRemoteUserUID": true, "customizations": true,
}

// UnmarshalJSON decodes into Raw and records any unrecognized top-level keys in
// Extra without failing, so a newer devcontainer.json still loads.
func (r *Raw) UnmarshalJSON(data []byte) error {
	type rawAlias Raw // avoid recursion
	var a rawAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = Raw(a)

	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	for k, v := range all {
		if !knownKeys[k] {
			if r.Extra == nil {
				r.Extra = map[string]json.RawMessage{}
			}
			r.Extra[k] = v
		}
	}
	return nil
}

// StringOrArray decodes a JSON string or array of strings into a slice.
type StringOrArray []string

func (s *StringOrArray) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '[' {
		return json.Unmarshal(data, (*[]string)(s))
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return fmt.Errorf("expected string or array of strings: %w", err)
	}
	*s = []string{one}
	return nil
}

// Command is a lifecycle command in any of its three forms: a string (run via a
// shell), an array (argv, no shell), or an object mapping names to either of
// those (entries run in parallel). Kind reports which form was used.
type Command struct {
	Kind CommandKind
	// Shell holds the single string form.
	Shell string
	// Argv holds the array form.
	Argv []string
	// Named holds the object form; each value is itself a Command of Shell or
	// Argv kind.
	Named map[string]Command
}

type CommandKind int

const (
	CommandNone CommandKind = iota
	CommandShell
	CommandArgv
	CommandNamed
)

func (c *Command) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		c.Kind = CommandNone
		return nil
	}
	switch data[0] {
	case '"':
		if err := json.Unmarshal(data, &c.Shell); err != nil {
			return err
		}
		c.Kind = CommandShell
	case '[':
		if err := json.Unmarshal(data, &c.Argv); err != nil {
			return err
		}
		c.Kind = CommandArgv
	case '{':
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		c.Named = make(map[string]Command, len(raw))
		for name, v := range raw {
			var sub Command
			if err := sub.UnmarshalJSON(v); err != nil {
				return fmt.Errorf("command %q: %w", name, err)
			}
			if sub.Kind == CommandNamed {
				return fmt.Errorf("command %q: nested object form is not allowed", name)
			}
			c.Named[name] = sub
		}
		c.Kind = CommandNamed
	default:
		return fmt.Errorf("command must be a string, array, or object, got %s", data)
	}
	return nil
}

// IsZero reports whether no command was specified.
func (c Command) IsZero() bool { return c.Kind == CommandNone }

// MarshalJSON emits the command in its original shape (string, array, or
// object) so `devc config` round-trips cleanly.
func (c Command) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case CommandShell:
		return json.Marshal(c.Shell)
	case CommandArgv:
		return json.Marshal(c.Argv)
	case CommandNamed:
		return json.Marshal(c.Named)
	default:
		return []byte("null"), nil
	}
}

// PortList decodes forwardPorts / appPort: an array whose elements are either
// numbers (3000) or "host:port" strings ("127.0.0.1:3000"), or a single such
// value. Elements are preserved as strings; Resolve parses them.
type PortList []string

func (p *PortList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var elems []json.RawMessage
	if data[0] == '[' {
		if err := json.Unmarshal(data, &elems); err != nil {
			return err
		}
	} else {
		elems = []json.RawMessage{data}
	}
	for _, e := range elems {
		e = bytes.TrimSpace(e)
		if len(e) > 0 && e[0] == '"' {
			var s string
			if err := json.Unmarshal(e, &s); err != nil {
				return err
			}
			*p = append(*p, s)
			continue
		}
		var n int
		if err := json.Unmarshal(e, &n); err != nil {
			return fmt.Errorf("port must be a number or string: %w", err)
		}
		*p = append(*p, strconv.Itoa(n))
	}
	return nil
}

// MountList decodes the "mounts" property: an array whose elements are either
// mount strings ("source=...,target=...,type=bind") or objects with
// source/target/type fields. Both are normalized to the string form.
type MountList []string

func (m *MountList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(data, &elems); err != nil {
		return err
	}
	for _, e := range elems {
		e = bytes.TrimSpace(e)
		if len(e) > 0 && e[0] == '"' {
			var s string
			if err := json.Unmarshal(e, &s); err != nil {
				return err
			}
			*m = append(*m, s)
			continue
		}
		var obj struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Type   string `json:"type"`
		}
		if err := json.Unmarshal(e, &obj); err != nil {
			return fmt.Errorf("mount must be a string or object: %w", err)
		}
		if obj.Type == "" {
			obj.Type = "bind"
		}
		s := fmt.Sprintf("type=%s,source=%s,target=%s", obj.Type, obj.Source, obj.Target)
		*m = append(*m, s)
	}
	return nil
}
