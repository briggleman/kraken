// Package share renders the public card a server share link opens (#475): the
// page a chat service unfurls (its Open Graph tags) and the page a friend lands
// on when they tap the link (with a copy control per join value).
//
// Everything here works from a Card, a view model that holds only what the
// link may show. The caller builds it from an allowlist; nothing in this
// package ever sees a server row, so nothing here can leak a field by
// accident.
package share

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// The four states a card shows. A server's lifecycle has more; the caller
// collapses them (see the api package).
const (
	StateRunning     = "running"
	StateStarting    = "starting"
	StateStopped     = "stopped"
	StateUnreachable = "unreachable"
)

// stateLabel is the chip text for each state.
var stateLabel = map[string]string{
	StateRunning:     "Running",
	StateStarting:    "Starting",
	StateStopped:     "Stopped",
	StateUnreachable: "Unreachable",
}

// themeColor is the colour a chat service paints the embed's side bar: the
// house's one light for a running server, sand for a stopped or starting one
// (not lit yet), and caution violet when the Panel cannot reach it.
var themeColor = map[string]string{
	StateRunning:     "#ffc266",
	StateStarting:    "#7d7160",
	StateStopped:     "#7d7160",
	StateUnreachable: "#9d8cff",
}

// The kinds of join value.
const (
	JoinAddress  = "address"
	JoinCode     = "invite_code"
	JoinPassword = "password"
)

// JoinValue is one value a player needs to connect.
type JoinValue struct {
	Kind  string // JoinAddress, JoinCode or JoinPassword
	Value string
}

// Label is the value's name on the card.
func (j JoinValue) Label() string {
	switch j.Kind {
	case JoinAddress:
		return "Address"
	case JoinCode:
		return "Invite code"
	default:
		return "Password"
	}
}

// Secret reports whether the value is masked on screen.
func (j JoinValue) Secret() bool { return j.Kind == JoinPassword }

// Card is everything a share link shows, and nothing else.
type Card struct {
	// ServerName is the server's display name.
	ServerName string
	// Game is the spec's name ("Windrose"); BannerURL its wide artwork.
	Game      string
	BannerURL string
	// State is one of the State constants.
	State string
	// Players and MaxPlayers are shown when PlayersKnown. MaxPlayers 0 means
	// the cap is unknown.
	Players, MaxPlayers int
	PlayersKnown        bool
	// Join holds the values this link may show, in the order a player needs
	// them; empty when the link shows status only.
	Join []JoinValue
	// Notes is the spec's one line on where the values go.
	Notes string
	// AsOf is when the reading on the card was taken.
	AsOf time.Time
	// SiteName and PageURL fill og:site_name and og:url; ImageURL is og:image.
	SiteName string
	PageURL  string
	ImageURL string
}

// StateLabel is the chip text.
func (c *Card) StateLabel() string { return stateLabel[c.State] }

// ThemeColor is the embed side-bar colour.
func (c *Card) ThemeColor() string { return themeColor[c.State] }

// PlayersText is "3 / 10", "3", or "" when the count is unknown.
func (c *Card) PlayersText() string {
	if !c.PlayersKnown {
		return ""
	}
	if c.MaxPlayers > 0 {
		return fmt.Sprintf("%d / %d", c.Players, c.MaxPlayers)
	}
	return fmt.Sprintf("%d", c.Players)
}

// Description is og:description: the state line, then the join values. A
// chat service shows it as plain text under the title, which is also the one
// place in a chat a player can select and copy the values from.
func (c *Card) Description() string {
	first := []string{c.Game}
	switch c.State {
	case StateUnreachable:
		first = append(first, "the panel can't reach this server right now")
	default:
		first = append(first, c.State)
		if c.State == StateRunning && c.PlayersKnown {
			if c.MaxPlayers > 0 {
				first = append(first, fmt.Sprintf("%d of %d players", c.Players, c.MaxPlayers))
			} else {
				first = append(first, fmt.Sprintf("%d players", c.Players))
			}
		}
	}
	out := strings.Join(first, " · ")
	if len(c.Join) > 0 {
		parts := make([]string, 0, len(c.Join))
		for _, j := range c.Join {
			parts = append(parts, strings.ToLower(j.Label())+" "+j.Value)
		}
		out += "\n" + strings.Join(parts, " · ")
	}
	return out
}

// AsOfText is the reading's time, in UTC so every reader sees the same clock.
func (c *Card) AsOfText() string { return c.AsOf.UTC().Format("15:04 UTC · 2 Jan") }

// CopyAll is the text the "copy all" control puts on the clipboard.
func (c *Card) CopyAll() string {
	lines := make([]string, 0, len(c.Join))
	for _, j := range c.Join {
		lines = append(lines, j.Label()+": "+j.Value)
	}
	return strings.Join(lines, "\n")
}

//go:embed page.html.tmpl
var pageSrc string

//go:embed static
var staticFS embed.FS

var page = template.Must(template.New("page").Parse(pageSrc))

// pageData is what the template sees: the card, or nil for the not-found page.
type pageData struct {
	Card *Card
}

// Render writes the card's page.
func Render(w io.Writer, c *Card) error { return page.Execute(w, pageData{Card: c}) }

// RenderNotFound writes the page for a link that does not open — unknown,
// revoked or expired, which it deliberately does not tell apart. It carries no
// Open Graph tags, so a chat service draws no card for it.
func RenderNotFound(w io.Writer) error { return page.Execute(w, pageData{}) }

// StaticHandler serves the page's stylesheet and script. The Panel's CSP
// allows neither inline styles nor inline scripts, so both are files.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // the embed is compiled in; a missing dir is a build bug
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})
}
