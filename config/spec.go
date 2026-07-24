package config

// Spec is the resolved, validated devcontainer configuration devc acts on. It
// is derived from Raw by Resolve and is the single source of truth for every
// later stage (bring-up, ssh wiring, hooks). `devc config` prints it as JSON.
type Spec struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       Kind   `json:"kind"`
	ConfigPath string `json:"configPath"`

	LocalWorkspaceFolder     string `json:"localWorkspaceFolder"`
	ContainerWorkspaceFolder string `json:"containerWorkspaceFolder"`

	Image   *ImageSpec   `json:"image,omitempty"`
	Compose *ComposeSpec `json:"compose,omitempty"`

	ContainerUser string            `json:"containerUser,omitempty"`
	RemoteUser    string            `json:"remoteUser,omitempty"`
	RemoteEnv     map[string]string `json:"remoteEnv,omitempty"`

	Ports []PortForward `json:"ports,omitempty"`

	OverrideCommand bool           `json:"overrideCommand"`
	ShutdownAction  ShutdownAction `json:"shutdownAction"`
	EnvProbe        EnvProbe       `json:"envProbe"`
	WaitFor         HookName       `json:"waitFor"`

	Hooks Hooks `json:"hooks"`
}

// Kind distinguishes the three bring-up strategies.
type Kind string

const (
	KindImage   Kind = "image"   // run from a prebuilt image
	KindBuild   Kind = "build"   // build a Dockerfile, then run
	KindCompose Kind = "compose" // bring up via docker-compose / podman
)

// ImageSpec holds the single-container bring-up details (image or build).
type ImageSpec struct {
	Image          string            `json:"image,omitempty"`
	Build          *BuildSpec        `json:"build,omitempty"`
	WorkspaceMount string            `json:"workspaceMount,omitempty"`
	Mounts         []string          `json:"mounts,omitempty"`
	ContainerEnv   map[string]string `json:"containerEnv,omitempty"`
	AppPorts       []PortForward     `json:"appPorts,omitempty"`
	RunArgs        []string          `json:"runArgs,omitempty"`
	Init           bool              `json:"init"`
	Privileged     bool              `json:"privileged"`
	CapAdd         []string          `json:"capAdd,omitempty"`
	SecurityOpt    []string          `json:"securityOpt,omitempty"`
}

// BuildSpec holds the `build` object for KindBuild.
type BuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args,omitempty"`
	Target     string            `json:"target,omitempty"`
	CacheFrom  []string          `json:"cacheFrom,omitempty"`
	Options    []string          `json:"options,omitempty"`
}

// ComposeSpec holds the compose bring-up details.
type ComposeSpec struct {
	// Files are absolute paths to the compose YAML files, in overlay order.
	Files []string `json:"files"`
	// Service is the container devc attaches to.
	Service string `json:"service"`
	// RunServices optionally narrows which services `up` starts (empty = all).
	RunServices []string `json:"runServices,omitempty"`
}

// PortForward is a resolved port mapping. HostPort defaults to ContainerPort
// when the config gives only a number; HostIP is optional.
type PortForward struct {
	HostIP        string `json:"hostIP,omitempty"`
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
}

// ShutdownAction mirrors the devcontainer.json shutdownAction values.
type ShutdownAction string

const (
	ShutdownNone        ShutdownAction = "none"
	ShutdownStopCtr     ShutdownAction = "stopContainer"
	ShutdownStopCompose ShutdownAction = "stopCompose"
)

// EnvProbe mirrors userEnvProbe.
type EnvProbe string

const (
	EnvProbeNone             EnvProbe = "none"
	EnvProbeLogin            EnvProbe = "loginShell"
	EnvProbeInteractive      EnvProbe = "interactiveShell"
	EnvProbeLoginInteractive EnvProbe = "loginInteractiveShell"
)

// HookName identifies a lifecycle hook, used by WaitFor.
type HookName string

const (
	HookInitialize    HookName = "initializeCommand"
	HookOnCreate      HookName = "onCreateCommand"
	HookUpdateContent HookName = "updateContentCommand"
	HookPostCreate    HookName = "postCreateCommand"
	HookPostStart     HookName = "postStartCommand"
	HookPostAttach    HookName = "postAttachCommand"
)

// Hooks bundles the resolved lifecycle commands in execution order.
type Hooks struct {
	Initialize    Command `json:"initialize,omitempty"`
	OnCreate      Command `json:"onCreate,omitempty"`
	UpdateContent Command `json:"updateContent,omitempty"`
	PostCreate    Command `json:"postCreate,omitempty"`
	PostStart     Command `json:"postStart,omitempty"`
	PostAttach    Command `json:"postAttach,omitempty"`
}
