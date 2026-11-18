// Package spec defines the declarative Game Specification — the "egg" equivalent.
// A Spec tells the platform how to install and run a dedicated game server:
// which Docker image(s) to use per platform, the install script, the startup
// command, the ports it needs, its user-editable variables, and any config-file
// templates. Specs are stored in Postgres and authored/edited through the UI.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

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

	// Backup, when set, declares WHAT a backup of this game captures. Omit it and
	// the Panel falls back to its built-in policy (whole data dir minus a
	// conservative ephemeral-only exclude list); declaring the block replaces that
	// policy wholesale — see Backup.
	Backup *Backup `json:"backup,omitempty"`

	// Join, when set, says what a player needs to connect: an address or an
	// invite code, and which value is the join password. A server share link
	// (#475) shows exactly these values and nothing else from the server's
	// settings, so the block is also the allowlist. Omit it and a share link
	// carries status and players only.
	Join *Join `json:"join,omitempty"`
}

// The join methods.
const (
	// JoinAddress: players connect to host:port — the server's public host and
	// the allocated port of the spec port named by Join.Port.
	JoinAddress = "address"
	// JoinInviteCode: players connect with a code the game issues (Windrose).
	// Join.Code names the setting or variable that holds it.
	JoinInviteCode = "invite_code"
)

// Join declares what a player needs to connect to a server of this spec.
type Join struct {
	// Method is "address" or "invite_code".
	Method string `json:"method"`
	// Port, for address, is the spec port NAME players connect to (usually the
	// game port, not the query port).
	Port string `json:"port,omitempty"`
	// Code, for invite_code, is the setting or variable key holding the code.
	Code string `json:"code,omitempty"`
	// Password is the setting or variable key holding the password a player
	// types to join. Never an admin or RCON password: a share link puts this
	// value in front of anyone the operator shares it with, and Validate refuses
	// the keys that are plainly not the join password. Omit it for a game with
	// no join password.
	Password string `json:"password,omitempty"`
	// Notes is one optional line telling a player where in the game the values
	// go ("join by invite code, then enter the password when asked").
	Notes string `json:"notes,omitempty"`
}

// joinMethods are the Join methods a share link can render.
var joinMethods = []string{JoinAddress, JoinInviteCode}

// joinNotesMax bounds Join.Notes: it is one line on a phone screen.
const joinNotesMax = 160

// declaresKey reports whether key is a variable or a settings field of the
// spec. A join key the spec does not declare is a typo that would leave the
// share page silently blank, so Validate refuses it.
func (s *Spec) declaresKey(key string) bool {
	for _, v := range s.Variables {
		if v.Key == key {
			return true
		}
	}
	for _, f := range s.Settings.fields() {
		if f.Key == key {
			return true
		}
	}
	return false
}

// adminLike reports whether a key names a credential that is plainly not a
// join password: anything admin-, RCON- or owner-flavoured.
func adminLike(key string) bool {
	k := strings.ToLower(key)
	for _, w := range []string{"admin", "rcon", "owner", "moderator", "operator"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// validateJoin rejects a join block a share link could not render, and any
// password key that is not the join password.
func (s *Spec) validateJoin() error {
	j := s.Join
	if j == nil {
		return nil
	}
	switch j.Method {
	case JoinAddress:
		if j.Port == "" {
			return fmt.Errorf("spec %q: join.port (the spec port name players connect to) is required for method address", s.Slug)
		}
		found := false
		for _, p := range s.Ports {
			if p.Name == j.Port {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("spec %q: join.port %q is not one of the spec's ports", s.Slug, j.Port)
		}
		if j.Code != "" {
			return fmt.Errorf("spec %q: join.code is only for method invite_code", s.Slug)
		}
	case JoinInviteCode:
		if j.Code == "" {
			return fmt.Errorf("spec %q: join.code (the setting or variable holding the code) is required for method invite_code", s.Slug)
		}
		if !s.declaresKey(j.Code) {
			return fmt.Errorf("spec %q: join.code %q is not a variable or setting of the spec", s.Slug, j.Code)
		}
		if j.Port != "" {
			return fmt.Errorf("spec %q: join.port is only for method address", s.Slug)
		}
	default:
		return fmt.Errorf("spec %q: join.method must be one of %v", s.Slug, joinMethods)
	}
	if j.Password != "" {
		if !s.declaresKey(j.Password) {
			// The key is left out of these two messages on purpose: it is only a
			// setting name, but it lives in a field called Password, and the
			// error reaches a log line (catalog seed), where code scanning
			// rightly refuses anything that looks like a credential. The
			// field path is enough to find it.
			return fmt.Errorf("spec %q: join.password is not a variable or setting of the spec", s.Slug)
		}
		if adminLike(j.Password) || (s.Query != nil && s.Query.Password == j.Password) {
			return fmt.Errorf("spec %q: join.password names an admin credential — name the password players type to join", s.Slug)
		}
	}
	if len(j.Notes) > joinNotesMax || strings.ContainsAny(j.Notes, "\r\n") {
		return fmt.Errorf("spec %q: join.notes must be one line of at most %d characters", s.Slug, joinNotesMax)
	}
	return nil
}

// Backup declares which parts of a server's data dir a backup captures.
//
// A backup is the game's SAVE DATA, not the reinstallable install tree: the
// 10–30 GB SteamCMD tree is recreatable with a reinstall, the saves are not, and
// tarring the whole tree is what drives the multi-hour backups, the mirror
// bandwidth, and the "write too long" archive race (live logs inside the install
// tree grow mid-capture). A spec that knows where its saves live says so here.
//
// Because a wrong include silently drops saves — the worst failure this system
// has — only declare a block when the save location is derivable from the spec
// itself (its config-file paths, startup args, env) or from authoritative game
// documentation. When in doubt, omit the block: the Panel's built-in policy
// captures everything except what is unambiguously ephemeral.
type Backup struct {
	// Include lists the paths to capture as doublestar globs (`**` spans any
	// number of path segments), matched against data-dir-relative POSIX paths:
	// "savegame/world.db", "Pal/Saved/SaveGames/0/Level.sav". An empty/omitted
	// list means "everything" — the excludes then carry the whole policy.
	Include []string `json:"include,omitempty"`
	// Exclude filters what Include selected, so it wins on a conflict. A pattern
	// that matches a directory prunes the walk there, which is how a game whose
	// saves sit inside a noisy tree (UE's `Saved/Logs`) stays cheap to archive.
	Exclude []string `json:"exclude,omitempty"`
}

// Patterns returns the block's include and exclude lists (nil-receiver safe, so
// callers can hand a spec's optional block straight through).
func (b *Backup) Patterns() (include, exclude []string) {
	if b == nil {
		return nil, nil
	}
	return b.Include, b.Exclude
}

// Platform binds a PlatformKind to the Docker image used to run it, with
// optional per-platform overrides for the install script and startup command.
// Overrides exist because the same game can need materially different
// commands per platform — e.g. a Windows-only server is installed with
// `steamcmd.exe … C:\data` on windows-native but with the Linux SteamCMD +
// `+@sSteamCmdForcePlatformType windows` on linux-wine, and launched via
// `xvfb-run wine …` there. Empty overrides fall back to the spec-level
// Install.Script / Startup.Command.
type Platform struct {
	Kind  PlatformKind `json:"kind"`
	Image string       `json:"image"`
	// InstallScript, when set, replaces Install.Script for servers placed on
	// this platform. Variable placeholders are substituted as usual.
	InstallScript string `json:"install_script,omitempty"`
	// StartupCommand, when set, replaces Startup.Command for servers placed on
	// this platform. Variable placeholders are substituted as usual.
	StartupCommand string `json:"startup_command,omitempty"`
	// SkipUpdateOnStart opts servers on THIS platform out of the pre-start
	// update pass (see Install.SkipUpdateOnStart) even when the spec-level flag
	// leaves it on — for the case where only one platform's install script is
	// not idempotent (e.g. a linux-wine entry that re-primes a Wine prefix).
	// It can only opt out: a platform cannot turn the pass back on for a spec
	// that opted out spec-wide.
	SkipUpdateOnStart bool `json:"skip_update_on_start,omitempty"`
}

// PlayerQuery declares how to read a server's online-player count.
type PlayerQuery struct {
	// Method is "a2s" (Steam A2S_INFO UDP query — most Steam servers),
	// "palworld-rest" (Palworld's REST players endpoint with admin Basic auth),
	// or "log" (the Agent follows the console and keeps a roster from the
	// join/leave lines — the only method that yields names).
	Method string `json:"method"`
	// Port is the spec port NAME to query: the A2S query port, or the REST API
	// port. The Panel resolves it to the allocated host port for the Agent.
	Port string `json:"port,omitempty"`
	// Password, for palworld-rest, names the setting/var key holding the admin
	// password (e.g. "AdminPassword"); the Panel resolves its value. Empty for A2S.
	Password string `json:"password,omitempty"`
	// JoinRegex and LeaveRegex, for "log", match the console line the game
	// prints when a player arrives and when one leaves. Each must capture the
	// player: (?P<name>…) is the display name, (?P<id>…) an optional stable
	// account id the roster keys on (so a renamed character does not count
	// twice); with no id the name is the key. RE2 syntax.
	JoinRegex  string `json:"join_regex,omitempty"`
	LeaveRegex string `json:"leave_regex,omitempty"`
	// MaxPlayers, for "log", is the game's player cap — a log never states one,
	// and the PLAYERS readout wants a denominator. 0 leaves it unknown.
	MaxPlayers int `json:"max_players,omitempty"`
	// MaxPlayersSetting, for "log", names a setting key whose value is the cap
	// (Enshrouded's slotCount) for games where the operator chooses it. The
	// Panel resolves the server's value; MaxPlayers is the fallback when the
	// setting is blank or not a number.
	MaxPlayersSetting string `json:"max_players_setting,omitempty"`
}

// queryMethods are the PlayerQuery methods the Agent implements.
var queryMethods = []string{"a2s", "palworld-rest", "log"}

// validateQuery rejects a query block the Agent could not act on. The failure
// modes are silent at runtime — an unknown method, a missing port, or a regex
// that never matches all read as "player count unknown" — so they are caught
// here, where the author sees them.
func (s *Spec) validateQuery() error {
	q := s.Query
	if q == nil {
		return nil
	}
	switch q.Method {
	case "a2s":
		if q.Port == "" {
			return fmt.Errorf("spec %q: query.port (the spec port name to query) is required for method a2s", s.Slug)
		}
	case "palworld-rest":
		if q.Port == "" || q.Password == "" {
			return fmt.Errorf("spec %q: query.port and query.password (setting keys) are required for method palworld-rest", s.Slug)
		}
	case "log":
		for _, f := range []struct{ field, rx string }{{"join_regex", q.JoinRegex}, {"leave_regex", q.LeaveRegex}} {
			if f.rx == "" {
				return fmt.Errorf("spec %q: query.%s is required for method log", s.Slug, f.field)
			}
			re, err := regexp.Compile(f.rx)
			if err != nil {
				return fmt.Errorf("spec %q: query.%s: %v", s.Slug, f.field, err)
			}
			if re.SubexpIndex("name") < 0 && re.SubexpIndex("id") < 0 {
				return fmt.Errorf("spec %q: query.%s must capture the player as (?P<name>…) or (?P<id>…)", s.Slug, f.field)
			}
		}
		if q.MaxPlayers < 0 {
			return fmt.Errorf("spec %q: query.max_players must be >= 0", s.Slug)
		}
	default:
		return fmt.Errorf("spec %q: query.method must be one of %v", s.Slug, queryMethods)
	}
	return nil
}

// Install describes the install/update phase, run in a short-lived container
// against the server's persistent volume before the runtime container starts.
//
// IT IS NOT ONE-SHOT. The Panel re-runs this phase before every
// operator-initiated start/restart so a server picks up depot updates instead
// of staying forever on the build SteamCMD pulled the day it was created, so:
//
// AN INSTALL SCRIPT MUST BE IDEMPOTENT. It runs against a fully installed,
// fully configured data dir carrying live save games. `steamcmd … app_update
// <id> validate +quit` is exactly that — a no-op on a current tree, a repair on
// a damaged one. A script that wipes the data dir, re-seeds a config file the
// operator has since edited, or unconditionally re-downloads an unversioned
// artifact is not, and must either be rewritten or opt out via
// SkipUpdateOnStart.
type Install struct {
	// Script is the shell command run inside the install container. Variable
	// placeholders (e.g. {{APP_ID}}) are substituted before execution — from
	// the server's CURRENT variables on every pass, so a variable edit lands on
	// the next start rather than waiting for a reinstall.
	Script string `json:"script"`
	// SkipUpdateOnStart opts this spec out of the pre-start update pass: the
	// install script then runs only at create time and on an explicit
	// reinstall, the pre-#307 behaviour. Default false — updates run — because
	// the opt-out costs a server its updates, and that is the rarer thing to
	// want. Set it only for an install that cannot be made idempotent.
	// Platform.SkipUpdateOnStart narrows the opt-out to one platform.
	SkipUpdateOnStart bool `json:"skip_update_on_start,omitempty"`
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
	//
	// It runs at CREATE and REINSTALL only — never on the pre-start update
	// pass. These scripts overlay files rather than update them (valheim's
	// `cp -rf …/. /data/` would clobber BepInEx/config on every restart, and
	// vrising's pulls an unpinned "latest" from Thunderstore), while a SteamCMD
	// `validate` only touches depot-manifest files and so leaves the
	// Doorstop/winhttp overlay intact across a game update.
	BepInExScript string `json:"bepinex_script,omitempty"`

	// UpdateCheck says how the Panel learns whether a server's installed build
	// is still the current one (#392). Usually omitted: a spec whose install
	// script runs `app_update` is checked against Steam with no edit (see
	// UpdateCheckFor). Declare it to name another branch or app id, or set
	// method none to opt out.
	UpdateCheck *UpdateCheck `json:"update_check,omitempty"`
}

// The update-check methods.
const (
	// UpdateCheckSteam compares the build id in the server's
	// steamapps/appmanifest_<app>.acf with the branch's current build, which
	// the Agent asks SteamCMD for.
	UpdateCheckSteam = "steam"
	// UpdateCheckNone opts out: the build is never checked and reads as
	// unsupported.
	UpdateCheckNone = "none"
)

// UpdateCheck is how a server's build is checked.
type UpdateCheck struct {
	// Method is steam or none.
	Method string `json:"method"`
	// AppID is the Steam app whose build is compared. Zero takes the spec's
	// steam_app_ids entry for the server's platform, which is the app the
	// install script was rendered with.
	AppID int `json:"app_id,omitempty"`
	// Branch is the depot branch the install follows; empty means public.
	Branch string `json:"branch,omitempty"`
}

// Steam reports whether the check is a Steam build-id check that can run: the
// method is steam and there is an app id to ask about.
func (u UpdateCheck) Steam() bool { return u.Method == UpdateCheckSteam && u.AppID > 0 }

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

// AllocMemoryMB is the memory a new server of this spec should be given.
//
// The recommended figure, when the spec states one — the minimum is the floor
// at which the game *boots*, not the figure it *runs* at, and a server placed
// at the floor is set up to fail: Valheim provisioned at its 2048MB minimum
// was OOM-killed (exit 137) generating its first world on a node with 21GB
// free. Specs distinguish the two numbers precisely so placement can use the
// honest one; the minimum remains the fallback for specs that state nothing.
func (r Resources) AllocMemoryMB() int {
	if r.RecommendedMemoryMB > 0 {
		return r.RecommendedMemoryMB
	}
	return r.MinMemoryMB
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

// InstallScriptFor returns the install script for the given platform kind:
// the platform's override when set, else the spec-level Install.Script.
func (s *Spec) InstallScriptFor(kind PlatformKind) string {
	for _, p := range s.Platforms {
		if p.Kind == kind && p.InstallScript != "" {
			return p.InstallScript
		}
	}
	return s.Install.Script
}

// StartupCommandFor returns the startup command for the given platform kind:
// the platform's override when set, else the spec-level Startup.Command.
func (s *Spec) StartupCommandFor(kind PlatformKind) string {
	for _, p := range s.Platforms {
		if p.Kind == kind && p.StartupCommand != "" {
			return p.StartupCommand
		}
	}
	return s.Startup.Command
}

// SkipUpdateOnStartFor reports whether servers on the given platform kind skip
// the pre-start update pass: the platform's own opt-out when set, else the
// spec-level Install.SkipUpdateOnStart. A platform can only opt out, never opt
// a spec-wide opt-out back in.
func (s *Spec) SkipUpdateOnStartFor(kind PlatformKind) bool {
	if s.Install.SkipUpdateOnStart {
		return true
	}
	for _, p := range s.Platforms {
		if p.Kind == kind && p.SkipUpdateOnStart {
			return true
		}
	}
	return false
}

// UpdateCheckFor resolves the build check for servers on the given platform
// kind.
//
// Without an update_check block the check is derived, so every Steam spec is
// covered without an edit: an install script (the platform's own, else the
// spec's) that runs `app_update` is checked against Steam, for the platform's
// steam_app_ids entry on the public branch. Any other script is method none —
// there is no manifest to read and no build to compare.
//
// A declared block wins. Its app id and branch fill in from the same
// defaults when it leaves them out, so `{method: steam, branch: beta}` is a
// whole declaration. A result with method steam but no app id cannot run
// (UpdateCheck.Steam is false); Validate refuses such a spec when the block is
// declared, and a derived one simply reads as unsupported.
func (s *Spec) UpdateCheckFor(kind PlatformKind) UpdateCheck {
	appID := s.SteamAppIDs[osFamily(kind)] // a nil map reads as 0
	if d := s.Install.UpdateCheck; d != nil {
		if d.Method != UpdateCheckSteam {
			return UpdateCheck{Method: UpdateCheckNone}
		}
		out := UpdateCheck{Method: UpdateCheckSteam, AppID: d.AppID, Branch: d.Branch}
		if out.AppID == 0 {
			out.AppID = appID
		}
		if out.Branch == "" {
			out.Branch = "public"
		}
		return out
	}
	if !strings.Contains(s.InstallScriptFor(kind), "app_update") {
		return UpdateCheck{Method: UpdateCheckNone}
	}
	return UpdateCheck{Method: UpdateCheckSteam, AppID: appID, Branch: "public"}
}

// osFamily is the steam_app_ids key for a platform kind: linux for a Linux
// server, windows for the Windows depot — which a linux-wine server runs too.
func osFamily(kind PlatformKind) string {
	if kind == LinuxNative {
		return "linux"
	}
	return "windows"
}

// validateUpdateCheck rejects a declared update_check block the Panel could
// not act on. A bad one does not fail loudly at runtime — it reads as unknown
// or unsupported on every server, forever — so the author is told here.
func (s *Spec) validateUpdateCheck() error {
	d := s.Install.UpdateCheck
	if d == nil {
		return nil
	}
	switch d.Method {
	case UpdateCheckNone:
		return nil
	case UpdateCheckSteam:
	default:
		return fmt.Errorf("spec %q: install.update_check.method must be %q or %q", s.Slug, UpdateCheckSteam, UpdateCheckNone)
	}
	if d.AppID < 0 {
		return fmt.Errorf("spec %q: install.update_check.app_id must be a Steam app id", s.Slug)
	}
	// The branch names a key under the app's "branches" in SteamCMD's app
	// info; a name with whitespace or a quote in it is no branch Steam has.
	if strings.ContainsAny(d.Branch, " \t\r\n\"'\\") {
		return fmt.Errorf("spec %q: install.update_check.branch %q is not a branch name", s.Slug, d.Branch)
	}
	for _, p := range s.Platforms {
		if !s.UpdateCheckFor(p.Kind).Steam() {
			return fmt.Errorf("spec %q: install.update_check.method is steam but %s has no app id — set install.update_check.app_id or steam_app_ids.%s",
				s.Slug, p.Kind, osFamily(p.Kind))
		}
	}
	return nil
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
		if v.Key == ValidateVar {
			return fmt.Errorf("spec %q: variable key %q is reserved: the Panel sets {{%s}} for each install pass", s.Slug, v.Key, ValidateVar)
		}
		seenVar[v.Key] = true
	}
	if s.Resources.MinMemoryMB < 0 {
		return fmt.Errorf("spec %q: resources.min_memory_mb must be >= 0", s.Slug)
	}
	if err := s.validateSettings(); err != nil {
		return err
	}
	if err := s.validateBackup(); err != nil {
		return err
	}
	if err := s.validateQuery(); err != nil {
		return err
	}
	if err := s.validateUpdateCheck(); err != nil {
		return err
	}
	if err := s.validateJoin(); err != nil {
		return err
	}
	return nil
}

// validateBackup rejects a backup block whose globs cannot compile or cannot
// possibly match. A pattern that never matches is not a cosmetic problem: an
// include list of only-broken patterns captures nothing, so a green backup would
// hold no saves at all. Better to refuse the spec at save time.
func (s *Spec) validateBackup() error {
	if s.Backup == nil {
		return nil
	}
	check := func(field string, pats []string) error {
		for i, p := range pats {
			switch {
			case strings.TrimSpace(p) == "":
				return fmt.Errorf("spec %q: backup.%s[%d]: pattern is empty", s.Slug, field, i)
			case strings.HasPrefix(p, "/"):
				return fmt.Errorf("spec %q: backup.%s[%d] (%q): patterns are data-dir-relative — drop the leading %q", s.Slug, field, i, p, "/")
			case strings.Contains(p, `\`):
				// Backslash is doublestar's escape character, so a Windows-style
				// path compiles fine and then silently matches nothing.
				return fmt.Errorf("spec %q: backup.%s[%d] (%q): use POSIX separators — a backslash never matches a data-dir path", s.Slug, field, i, p)
			case !doublestar.ValidatePattern(p):
				return fmt.Errorf("spec %q: backup.%s[%d] (%q): not a valid glob pattern", s.Slug, field, i, p)
			}
		}
		return nil
	}
	if err := check("include", s.Backup.Include); err != nil {
		return err
	}
	return check("exclude", s.Backup.Exclude)
}
