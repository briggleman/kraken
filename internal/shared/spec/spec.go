// Package spec defines the declarative Game Specification — the "egg" equivalent.
// A Spec tells the platform how to install and run a dedicated game server:
// which Docker image(s) to use per platform, the install script, the startup
// command, the ports it needs, its user-editable variables, and any config-file
// templates. Specs are stored in Postgres and authored/edited through the UI.
package spec

import "fmt"

// PlatformKind identifies how a game server is executed. The scheduler considers
// candidate kinds in the priority order declared on a Spec; native Linux is
// preferred whenever a Linux dedicated server exists for the game.
type PlatformKind string

const (
	// LinuxNative runs a Linux dedicated server in a Linux container.
	LinuxNative PlatformKind = "linux-native"
	// LinuxWine runs a Windows-only dedicated server inside a Linux container via Wine.
	LinuxWine PlatformKind = "linux-wine"
	// WindowsNative runs a Windows dedicated server in a Windows container on a Windows node.
	WindowsNative PlatformKind = "windows-native"
)

// Valid reports whether k is a recognized platform kind.
func (k PlatformKind) Valid() bool {
	switch k {
	case LinuxNative, LinuxWine, WindowsNative:
		return true
	default:
		return false
	}
}

// Protocol is a transport protocol for a port allocation.
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Valid reports whether p is a recognized protocol.
func (p Protocol) Valid() bool { return p == TCP || p == UDP }

// Spec is a complete, versioned game definition.
type Spec struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`

	// BannerURL and IconURL are optional artwork shown in the UI: the banner is a
	// wide hero image behind the server header; the icon is a small square badge
	// next to the game name. Both are plain image URLs carried by the spec.
	BannerURL string `json:"banner_url,omitempty"`
	IconURL   string `json:"icon_url,omitempty"`

	// SteamAppIDs maps platform OS ("linux"/"windows") to the SteamCMD app id of
	// the dedicated server. A spec may define one or both.
	SteamAppIDs map[string]int `json:"steam_app_ids,omitempty"`

	// Platforms lists the execution targets in scheduler priority order.
	Platforms []Platform `json:"platforms"`

	Install     Install      `json:"install"`
	Startup     Startup      `json:"startup"`
	Variables   []Variable   `json:"variables,omitempty"`
	Ports       []Port       `json:"ports"`
	Settings    Settings     `json:"settings,omitempty"`
	ConfigFiles []ConfigFile `json:"config_files,omitempty"`
	Resources   Resources    `json:"resources"`

	// Query, when set, tells the Agent how to read the live online-player count
	// (shown on the server detail + fleet). Omit for games with no query support.
	Query *PlayerQuery `json:"query,omitempty"`
}

// Platform binds a PlatformKind to the Docker image used to run it.
type Platform struct {
	Kind  PlatformKind `json:"kind"`
	Image string       `json:"image"`
}

// PlayerQuery declares how to read a server's online-player count.
type PlayerQuery struct {
	// Method is "a2s" (Steam A2S_INFO UDP query — most Steam servers) or
	// "palworld-rest" (Palworld's REST players endpoint with admin Basic auth).
	Method string `json:"method"`
	// Port is the spec port NAME to query: the A2S query port, or the REST API
	// port. The Panel resolves it to the allocated host port for the Agent.
	Port string `json:"port"`
	// Password, for palworld-rest, names the setting/var key holding the admin
	// password (e.g. "AdminPassword"); the Panel resolves its value. Empty for A2S.
	Password string `json:"password,omitempty"`
}

// Install describes the one-shot install/update phase, run in a short-lived
// container against the server's persistent volume before the runtime container
// starts.
type Install struct {
	// Script is the shell command run inside the install container. Variable
	// placeholders (e.g. {{APP_ID}}) are substituted before execution.
	Script string `json:"script"`
	// RequiresSteamLogin is true for app ids that need a real Steam account
	// (vs. anonymous login). The Panel brokers credentials + Steam Guard 2FA.
	RequiresSteamLogin bool `json:"requires_steam_login,omitempty"`

	// BepInExCompatible declares that this game supports BepInEx mods (a Unity
	// modding framework). When true, the deploy dialog surfaces an opt-in "Install
	// BepInEx mod support" toggle; when chosen, BepInExScript is appended to the
	// install and the server launches via Startup.BepInExCommand (the loader).
	BepInExCompatible bool `json:"bepinex_compatible,omitempty"`
	// BepInExScript is appended after Script (vanilla install runs first) to
	// download + unpack BepInEx into the data dir. Only used when a server is
	// deployed with BepInEx enabled. Variable placeholders are substituted.
	BepInExScript string `json:"bepinex_script,omitempty"`
}

// StopKind selects how a running server is asked to shut down gracefully.
type StopKind string

const (
	// StopSignal sends an OS signal (e.g. SIGINT) to the container's main process.
	StopSignal StopKind = "signal"
	// StopCommand writes a command to the server console/stdin (e.g. "quit").
	StopCommand StopKind = "command"
)

// Stop describes how to gracefully stop a running server.
type Stop struct {
	Type  StopKind `json:"type"`
	Value string   `json:"value"`
}

// Startup describes how the runtime container launches and is monitored.
type Startup struct {
	// Command is the startup command template; variable placeholders are substituted.
	Command string `json:"command"`
	// BepInExCommand is the alternate startup command used when a server is
	// deployed with BepInEx enabled — it must launch through the BepInEx/Doorstop
	// loader (e.g. ./run_bepinex.sh) so plugins actually load. Falls back to
	// Command when empty. Variable placeholders are substituted.
	BepInExCommand string `json:"bepinex_command,omitempty"`
	// ReadyRegex matches a console line indicating the server is fully started;
	// it flips the lifecycle state to running and powers the crash watchdog.
	ReadyRegex string `json:"ready_regex,omitempty"`
	Stop       Stop   `json:"stop"`
	// Restart configures the crash watchdog's auto-restart behavior.
	Restart RestartPolicy `json:"restart,omitempty"`
}

// RestartPolicy controls whether the Agent's crash watchdog automatically
// restarts a server after an unexpected exit (one not caused by an operator
// stop/kill). It does not affect graceful stops.
type RestartPolicy struct {
	// OnCrash enables auto-restart after an unexpected exit.
	OnCrash bool `json:"on_crash,omitempty"`
	// MaxRetries caps consecutive crash-restarts before the server is left in the
	// crashed state. A manual start/restart resets the counter. 0 → agent default.
	MaxRetries int `json:"max_retries,omitempty"`
}

// Variable is a user-editable launch option surfaced in the UI. Rules drive
// server-side validation (e.g. "int|min:1|max:64").
type Variable struct {
	Key          string `json:"key"`
	Label        string `json:"label,omitempty"`
	Default      string `json:"default"`
	Rules        string `json:"rules,omitempty"`
	UserEditable bool   `json:"user_editable"`
}

// Port describes a network port the server needs. Required ports must be
// allocated for the server to start.
type Port struct {
	Name     string   `json:"name"`
	Protocol Protocol `json:"protocol"`
	Default  int      `json:"default"`
	Required bool     `json:"required,omitempty"`
}

// ConfigFile describes a game config file generated into the server volume from
// the server's settings values. The Format selects how values are emitted; see
// settings.go for the adapters and rendering.
type ConfigFile struct {
	Path string `json:"path"`
	// Format selects the emitter: source-cvar | ini | properties | keyvalue |
	// json | env | template.
	Format ConfigFormat `json:"format"`
	// Bindings maps an output key to the setting it draws from (adapter formats).
	Bindings map[string]Binding `json:"bindings,omitempty"`
	// Section is an optional section header for the ini format.
	Section string `json:"section,omitempty"`
	// Template is the body for format=template (Go text/template).
	Template string `json:"template,omitempty"`
}

// Resources captures the memory expectations the scheduler uses for placement.
type Resources struct {
	MinMemoryMB         int `json:"min_memory_mb"`
	RecommendedMemoryMB int `json:"recommended_memory_mb,omitempty"`
}

// PlatformKinds returns the spec's platform kinds in priority order.
func (s *Spec) PlatformKinds() []PlatformKind {
	kinds := make([]PlatformKind, 0, len(s.Platforms))
	for _, p := range s.Platforms {
		kinds = append(kinds, p.Kind)
	}
	return kinds
}

// ImageFor returns the Docker image declared for the given platform kind.
func (s *Spec) ImageFor(kind PlatformKind) (string, bool) {
	for _, p := range s.Platforms {
		if p.Kind == kind {
			return p.Image, true
		}
	}
	return "", false
}

// Validate performs structural validation beyond JSON Schema: it enforces
// invariants the rest of the system relies on (non-empty identifiers, known
// enums, at least one platform and one port, etc.).
func (s *Spec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("spec: name is required")
	}
	if s.Slug == "" {
		return fmt.Errorf("spec: slug is required")
	}
	if len(s.Platforms) == 0 {
		return fmt.Errorf("spec %q: at least one platform is required", s.Slug)
	}
	seenKind := make(map[PlatformKind]bool, len(s.Platforms))
	for i, p := range s.Platforms {
		if !p.Kind.Valid() {
			return fmt.Errorf("spec %q: platform[%d]: unknown kind %q", s.Slug, i, p.Kind)
		}
		if seenKind[p.Kind] {
			return fmt.Errorf("spec %q: platform kind %q declared more than once", s.Slug, p.Kind)
		}
		seenKind[p.Kind] = true
		if p.Image == "" {
			return fmt.Errorf("spec %q: platform[%d] (%s): image is required", s.Slug, i, p.Kind)
		}
	}
	if s.Install.Script == "" {
		return fmt.Errorf("spec %q: install.script is required", s.Slug)
	}
	if s.Startup.Command == "" {
		return fmt.Errorf("spec %q: startup.command is required", s.Slug)
	}
	switch s.Startup.Stop.Type {
	case StopSignal, StopCommand:
		if s.Startup.Stop.Value == "" {
			return fmt.Errorf("spec %q: startup.stop.value is required", s.Slug)
		}
	default:
		return fmt.Errorf("spec %q: startup.stop.type must be %q or %q", s.Slug, StopSignal, StopCommand)
	}
	if len(s.Ports) == 0 {
		return fmt.Errorf("spec %q: at least one port is required", s.Slug)
	}
	seenPort := make(map[string]bool, len(s.Ports))
	for i, p := range s.Ports {
		if p.Name == "" {
			return fmt.Errorf("spec %q: port[%d]: name is required", s.Slug, i)
		}
		if seenPort[p.Name] {
			return fmt.Errorf("spec %q: duplicate port name %q", s.Slug, p.Name)
		}
		seenPort[p.Name] = true
		if !p.Protocol.Valid() {
			return fmt.Errorf("spec %q: port %q: protocol must be tcp or udp", s.Slug, p.Name)
		}
		if p.Default < 1 || p.Default > 65535 {
			return fmt.Errorf("spec %q: port %q: default %d out of range", s.Slug, p.Name, p.Default)
		}
	}
	seenVar := make(map[string]bool, len(s.Variables))
	for i, v := range s.Variables {
		if v.Key == "" {
			return fmt.Errorf("spec %q: variable[%d]: key is required", s.Slug, i)
		}
		if seenVar[v.Key] {
			return fmt.Errorf("spec %q: duplicate variable key %q", s.Slug, v.Key)
		}
		seenVar[v.Key] = true
	}
	if s.Resources.MinMemoryMB < 0 {
		return fmt.Errorf("spec %q: resources.min_memory_mb must be >= 0", s.Slug)
	}
	if err := s.validateSettings(); err != nil {
		return err
	}
	return nil
}
