package steam

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Manifest is what the Panel reads out of a server's
// steamapps/appmanifest_<appid>.acf: which app the install tree holds and
// which build of it SteamCMD last finished writing (#392).
type Manifest struct {
	AppID   string
	Name    string
	BuildID string
	// LastUpdated is when SteamCMD last finished an update of the tree, Unix
	// seconds; 0 when the manifest does not say.
	LastUpdated int64
}

// ParseAppManifest reads an appmanifest_*.acf, the KeyValues ("VDF") file
// SteamCMD keeps beside every app it installs:
//
//	"AppState"
//	{
//		"appid"		"2394010"
//		"name"		"Palworld Dedicated Server"
//		"LastUpdated"		"1728400000"
//		"buildid"		"25247047"
//		"InstalledDepots" { … }
//	}
//
// buildid is the installed build, the number the build check compares with
// the branch's current one. Keys are matched without regard to case, the way
// Steam reads them, and the nested blocks (InstalledDepots, UserConfig, …) are
// parsed only to be skipped. A manifest with no AppState block or no buildid
// is an error: a tree whose build cannot be read must never compare equal to
// anything.
func ParseAppManifest(data []byte) (Manifest, error) {
	p := &acfParser{src: string(data)}
	root, err := p.object(true)
	if err != nil {
		return Manifest{}, err
	}
	state := root.child("AppState")
	if state == nil {
		return Manifest{}, errors.New("appmanifest: no AppState block")
	}
	m := Manifest{
		AppID:   state.value("appid"),
		Name:    state.value("name"),
		BuildID: state.value("buildid"),
	}
	if m.BuildID == "" {
		return Manifest{}, errors.New("appmanifest: AppState has no buildid")
	}
	if v := state.value("LastUpdated"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Manifest{}, fmt.Errorf("appmanifest: LastUpdated %q is not a Unix time", v)
		}
		m.LastUpdated = n
	}
	return m, nil
}

// acfObject is one { … } block of a KeyValues file: its string values and its
// nested blocks, keyed by lower-cased name. A repeated key keeps its first
// value, which is what Steam's own reader does.
type acfObject struct {
	values   map[string]string
	children map[string]*acfObject
}

func (o *acfObject) value(key string) string { return o.values[strings.ToLower(key)] }

func (o *acfObject) child(key string) *acfObject { return o.children[strings.ToLower(key)] }

// acfParser walks a KeyValues document. The names are prefixed so the steam
// package can hold the app_info_print parser beside this one without the two
// colliding.
type acfParser struct {
	src string
	pos int
}

// The token kinds a KeyValues document is made of.
const (
	acfEOF = iota
	acfString
	acfOpen
	acfClose
)

// object parses key/value pairs up to the closing brace, or to the end of the
// input for the document's top level.
func (p *acfParser) object(top bool) (*acfObject, error) {
	obj := &acfObject{values: map[string]string{}, children: map[string]*acfObject{}}
	for {
		kind, key, err := p.next()
		if err != nil {
			return nil, err
		}
		switch kind {
		case acfEOF:
			if !top {
				return nil, errors.New("appmanifest: unexpected end of file inside a block")
			}
			return obj, nil
		case acfClose:
			if top {
				return nil, errors.New("appmanifest: unbalanced closing brace")
			}
			return obj, nil
		case acfOpen:
			return nil, errors.New("appmanifest: block without a key")
		}
		vkind, val, err := p.next()
		if err != nil {
			return nil, err
		}
		lk := strings.ToLower(key)
		switch vkind {
		case acfString:
			if _, seen := obj.values[lk]; !seen {
				obj.values[lk] = val
			}
		case acfOpen:
			child, err := p.object(false)
			if err != nil {
				return nil, err
			}
			if _, seen := obj.children[lk]; !seen {
				obj.children[lk] = child
			}
		default:
			return nil, fmt.Errorf("appmanifest: key %q has no value", key)
		}
	}
}

// next returns the next token, skipping whitespace and // comments. Strings
// are quoted with the usual backslash escapes (a Windows SteamCMD writes its
// LauncherPath as "C:\\steamcmd\\steamcmd.exe"); a bare word is accepted too,
// as Steam's reader does.
func (p *acfParser) next() (int, string, error) {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], "\xef\xbb\xbf"):
			p.pos += 3 // a UTF-8 byte-order mark
		case strings.HasPrefix(p.src[p.pos:], "//"):
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '{':
			p.pos++
			return acfOpen, "", nil
		case c == '}':
			p.pos++
			return acfClose, "", nil
		case c == '"':
			return p.quoted()
		default:
			start := p.pos
			for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n{}\"", rune(p.src[p.pos])) {
				p.pos++
			}
			return acfString, p.src[start:p.pos], nil
		}
	}
	return acfEOF, "", nil
}

// quoted reads a "…" string starting at the opening quote.
func (p *acfParser) quoted() (int, string, error) {
	p.pos++ // the opening quote
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			return acfString, b.String(), nil
		case '\\':
			if p.pos+1 < len(p.src) {
				p.pos++
				// KeyValues escapes only these four, as appinfo.go reads them.
				// Any other backslash is a literal one: the separator in a
				// Windows path ("bin\win64").
				switch e := p.src[p.pos]; e {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(e)
				default:
					b.WriteByte('\\')
					b.WriteByte(e)
				}
				p.pos++
				continue
			}
		}
		b.WriteByte(c)
		p.pos++
	}
	return 0, "", errors.New("appmanifest: unterminated string")
}
