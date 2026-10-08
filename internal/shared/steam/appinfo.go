// Package steam reads what Steam says about an app's builds: the build a
// branch currently ships, from SteamCMD's app_info_print, and the build a
// server has installed, from the appmanifest SteamCMD leaves in its tree.
// Comparing the two is how the Panel decides whether a start needs the
// multi-minute app_update pass at all (#392).
//
// Both are Valve KeyValues ("VDF") text. Nothing here runs SteamCMD or touches
// a filesystem; callers hand in the text, so the parsers are testable against
// real captured output.
package steam

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Branch is one depot branch of an app, as app_info_print reports it under
// "depots" → "branches".
type Branch struct {
	BuildID string
	// TimeUpdated is when the branch last moved to a build (Steam's
	// timeupdated), and TimeBuildUpdated when that build itself was made
	// (timebuildupdated). Both are Unix seconds, 0 when absent.
	TimeUpdated      int64
	TimeBuildUpdated int64
	// Description is the label a non-public branch carries ("Previous
	// stable"); "" for public.
	Description string
}

// AppInfo is what one app_info_print block says about one app.
type AppInfo struct {
	AppID string
	// Name is common.name ("Palworld Dedicated Server"), "" when absent.
	Name string
	// ChangeNumber is the PICS change number from the "AppID : …" header line,
	// 0 when the header did not carry one.
	ChangeNumber int64
	// Branches is keyed by branch name ("public", "default_old", betas). It is
	// nil when the block has no "branches" key at all, which is how SteamCMD
	// answers on a fresh home before its client config has loaded; the caller
	// asks again rather than treating it as "no builds".
	Branches map[string]Branch
	// Unknown reports an empty block, `"<id>" { }`: SteamCMD's answer for an
	// app id Steam has no record of (verified with 999999999), and plausibly
	// for one the anonymous account may not see.
	Unknown bool
	// Truncated reports that the block ended before its closing brace (the
	// session died or the output was cut off). Branches then holds only the
	// branches whose own block closed, so a half-read branch never reports a
	// build.
	Truncated bool
}

// ParseAppInfo parses the stdout of a SteamCMD session that ran one or more
// `+app_info_print <id>` commands, keyed by app id.
//
// The session's preamble (SteamCMD's self-update progress, "Loading Steam
// API...OK", the login lines) and its epilogue are skipped: each app's block
// begins at its "AppID : <id>, change number : …" header and runs to the next
// header or the end of the output. ANSI escape sequences, which SteamCMD
// writes into stdout (sometimes at the very start of a header line), are
// stripped first, and CRLF line endings are accepted.
//
// An error means the output held no app block at all: the login failed, Steam
// was unreachable, or SteamCMD never got as far as printing. A block that is
// present but incomplete is reported through AppInfo (Branches nil, Truncated)
// rather than as an error, so one bad app does not hide the others.
func ParseAppInfo(output string) (map[string]AppInfo, error) {
	text := stripAppInfoANSI(output)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	headers := appInfoHeaderRE.FindAllStringSubmatchIndex(text, -1)
	if len(headers) == 0 {
		return nil, fmt.Errorf("no app info in the steamcmd output")
	}
	apps := make(map[string]AppInfo, len(headers))
	for i, h := range headers {
		end := len(text)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		info := AppInfo{AppID: text[h[2]:h[3]]}
		if h[4] >= 0 {
			info.ChangeNumber, _ = strconv.ParseInt(text[h[4]:h[5]], 10, 64)
		}
		parseAppInfoBlock(text[h[1]:end], &info)
		apps[info.AppID] = info
	}
	return apps, nil
}

// appInfoHeaderRE matches the line SteamCMD prints before each app's block:
//
//	AppID : 2394010, change number : 39612183/39612183, last change : Thu Oct  8 14:44:02 2026
//
// The change number is optional so a header from a SteamCMD that words the
// rest of the line differently still opens a block.
var appInfoHeaderRE = regexp.MustCompile(`(?m)^[ \t]*AppID\s*:\s*(\d+)(?:,\s*change number\s*:\s*(\d+))?[^\n]*$`)

// appInfoANSIRE matches ANSI escape sequences: CSI (`ESC [ … m` and friends),
// OSC (`ESC ] … BEL`), and the two-byte forms. The same pattern the Panel's
// install log strips with; it is repeated here rather than shared because the
// Panel's copy lives in its API package.
var appInfoANSIRE = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

func stripAppInfoANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return appInfoANSIRE.ReplaceAllString(s, "")
}

// parseAppInfoBlock reads one app's KeyValues block into info. The block's
// top-level key is the app id; when it names a different app than the header
// (never seen, but cheap to respect) the key wins, since it is what the
// branches below it belong to.
func parseAppInfoBlock(block string, info *AppInfo) {
	lx := &appInfoLexer{src: block}
	key, ok := lx.next()
	if !ok || key.kind != appInfoString {
		// A header with nothing after it: the session ended right there.
		info.Truncated = true
		return
	}
	if _, err := strconv.ParseUint(key.text, 10, 64); err == nil {
		info.AppID = key.text
	}
	root, closed := parseAppInfoValue(lx)
	if !closed {
		info.Truncated = true
	}
	if root == nil || root.children == nil {
		return
	}
	if closed && len(root.children) == 0 {
		info.Unknown = true
		return
	}
	if common := root.child("common"); common != nil {
		info.Name = common.child("name").str()
	}
	branches := root.child("depots").child("branches")
	if branches == nil || branches.children == nil {
		return
	}
	info.Branches = make(map[string]Branch, len(branches.children))
	for _, kv := range branches.children {
		b := kv.node
		if b == nil || b.children == nil || !b.closed {
			continue // a scalar, or a branch block the output cut off
		}
		info.Branches[kv.key] = Branch{
			BuildID:          b.child("buildid").str(),
			TimeUpdated:      b.child("timeupdated").int64(),
			TimeBuildUpdated: b.child("timebuildupdated").int64(),
			Description:      strings.TrimSpace(b.child("description").str()),
		}
	}
}

// appInfoNode is one KeyValues value: a string, or an object whose children
// keep their order. closed records whether an object's closing brace was
// read.
type appInfoNode struct {
	value    string
	children []appInfoPair // nil for a string value
	closed   bool
}

type appInfoPair struct {
	key  string
	node *appInfoNode
}

// child returns the first child named key, matched exactly and then without
// regard to case (KeyValues keys are case-insensitive), or nil. It is safe on
// a nil node, so lookups chain.
func (n *appInfoNode) child(key string) *appInfoNode {
	if n == nil {
		return nil
	}
	for _, kv := range n.children {
		if kv.key == key {
			return kv.node
		}
	}
	for _, kv := range n.children {
		if strings.EqualFold(kv.key, key) {
			return kv.node
		}
	}
	return nil
}

func (n *appInfoNode) str() string {
	if n == nil || n.children != nil {
		return ""
	}
	return n.value
}

func (n *appInfoNode) int64() int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(n.str()), 10, 64)
	return v
}

// parseAppInfoValue reads the value that follows a key: a string, or an
// object up to its matching brace. closed is false when the input ran out
// first, anywhere inside the value.
func parseAppInfoValue(lx *appInfoLexer) (*appInfoNode, bool) {
	tok, ok := lx.next()
	if !ok {
		return nil, false
	}
	switch tok.kind {
	case appInfoString:
		return &appInfoNode{value: tok.text}, true
	case appInfoClose:
		// A key with no value before the enclosing object closed: malformed,
		// and the caller's object is over. Report it as unclosed so nothing
		// below is trusted.
		return nil, false
	}
	obj := &appInfoNode{children: []appInfoPair{}}
	for {
		tok, ok := lx.next()
		if !ok {
			return obj, false
		}
		switch tok.kind {
		case appInfoClose:
			obj.closed = true
			return obj, true
		case appInfoOpen:
			// An object with no key: not KeyValues. Treat the rest as
			// unreadable rather than guess at its shape.
			return obj, false
		}
		child, closed := parseAppInfoValue(lx)
		if child != nil {
			obj.children = append(obj.children, appInfoPair{key: tok.text, node: child})
		}
		if !closed {
			return obj, false
		}
	}
}

type appInfoTokenKind int

const (
	appInfoString appInfoTokenKind = iota // quoted or bare
	appInfoOpen                           // {
	appInfoClose                          // }
)

type appInfoToken struct {
	kind appInfoTokenKind
	text string
}

// appInfoLexer splits KeyValues text into strings and braces. Quoted strings
// may hold whitespace, tabs included (Valheim's branch descriptions start
// with one), and the escapes \" \\ \n \t. Bare words and // comments are
// accepted because KeyValues allows them, though app_info_print uses
// neither.
type appInfoLexer struct {
	src string
	pos int
}

func (lx *appInfoLexer) next() (appInfoToken, bool) {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			lx.pos++
		case c == '/' && strings.HasPrefix(lx.src[lx.pos:], "//"):
			if nl := strings.IndexByte(lx.src[lx.pos:], '\n'); nl >= 0 {
				lx.pos += nl + 1
			} else {
				lx.pos = len(lx.src)
			}
		case c == '{':
			lx.pos++
			return appInfoToken{kind: appInfoOpen}, true
		case c == '}':
			lx.pos++
			return appInfoToken{kind: appInfoClose}, true
		case c == '"':
			return lx.quoted()
		default:
			start := lx.pos
			for lx.pos < len(lx.src) && !strings.ContainsRune(" \t\n\r{}\"", rune(lx.src[lx.pos])) {
				lx.pos++
			}
			return appInfoToken{kind: appInfoString, text: lx.src[start:lx.pos]}, true
		}
	}
	return appInfoToken{}, false
}

// quoted reads a quoted string. An unterminated one is the output ending
// mid-value, so it reports no token, which the parser treats as truncation.
//
// A Windows path whose last character is a backslash ("bin\") would read as
// an escaped quote and swallow the rest of the line. Values never span lines,
// so reaching a newline after an escaped quote means exactly that: the string
// is re-read from its opening quote with backslashes taken literally.
func (lx *appInfoLexer) quoted() (appInfoToken, bool) {
	lx.pos++ // opening quote
	start := lx.pos
	escapedQuote := false
	var b strings.Builder
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '"':
			lx.pos++
			return appInfoToken{kind: appInfoString, text: b.String()}, true
		case c == '\n' && escapedQuote:
			if end := strings.IndexByte(lx.src[start:], '"'); end >= 0 && start+end < lx.pos {
				lx.pos = start + end + 1
				return appInfoToken{kind: appInfoString, text: lx.src[start : start+end]}, true
			}
			b.WriteByte(c)
			lx.pos++
		case c == '\\' && lx.pos+1 < len(lx.src):
			escapedQuote = escapedQuote || lx.src[lx.pos+1] == '"'
			lx.pos += 2
			switch e := lx.src[lx.pos-1]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
			lx.pos++
		}
	}
	lx.pos = len(lx.src)
	return appInfoToken{}, false
}
