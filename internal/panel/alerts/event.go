// Package alerts turns what the Panel notices about its fleet into push alerts
// (#348, docs/design/push-alerts.md). The reconcilers and the backup paths say
// what happened — a node went offline, a server crashed, the watchdog brought
// one back, a player joined, a backup failed — as an Event; the Dispatcher
// decides, per registered device and at the moment of sending, whether its
// user may know and whether the device asked to, then seals the alert to the
// device and hands it to the relay. The trackers here hold the little memory
// the sources need to see a change rather than a state: the last watchdog
// count, the last roster, the last node status.
//
// The package does no HTTP and starts no pollers. It never sees a session: the
// user and role behind each device are read at event time, so a permission
// change applies to the very next alert.
package alerts

import (
	"fmt"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
)

// ServerRef is what an event needs to know about a server: enough to name it,
// thread it, and decide who may see it.
type ServerRef struct {
	ID   string
	Name string
	// OwnerID is the server's owner, for the same ownership rule the API
	// applies (store.MayAccessServer).
	OwnerID string
	// NodeID is the node the server is on, carried in the payload so the app
	// can open it; empty for a server on no node.
	NodeID string
}

// Event is one thing that happened, ready to become an alert.
type Event struct {
	// Class is push.ClassAttend, ClassHealed or ClassAlive.
	Class string
	// Name is the event, one of the push.Event* constants.
	Name string
	// Server is set for a server event; NodeID and NodeName for a node event
	// (and NodeID for a server event, from Server.NodeID).
	Server   ServerRef
	NodeID   string
	NodeName string
	// Body is the sentence the phone shows, in the house voice.
	Body string
	// At is when it happened. The relay's two-minute window runs from here.
	At time.Time
}

// serverEvent reports whether the event is about a server (as opposed to a
// node, or the test alert).
func (e Event) serverEvent() bool { return e.Server.ID != "" }

// nodeEvent reports whether the event is about a node alone.
func (e Event) nodeEvent() bool { return e.Server.ID == "" && e.NodeID != "" }

// Payload is the plaintext the device receives. Every event of a server
// threads under the server, so its crash, its restarts and its players group
// together on the phone; a node event threads under the node.
func (e Event) Payload() push.Payload {
	p := push.Payload{
		V: push.PayloadVersion, Class: e.Class, Event: e.Name,
		Body: e.Body, TSms: e.At.UnixMilli(),
	}
	switch {
	case e.serverEvent():
		p.ServerID, p.NodeID, p.Title = e.Server.ID, e.Server.NodeID, e.Server.Name
		p.Thread = "server:" + e.Server.ID
	case e.nodeEvent():
		p.NodeID, p.Title = e.NodeID, e.NodeName
		p.Thread = "node:" + e.NodeID
	default:
		p.Title, p.Thread = "Kraken", "test"
	}
	return p
}

// subject names what the event is about, for the audit log.
func (e Event) subject() string {
	switch {
	case e.serverEvent():
		return "server " + e.Server.Name
	case e.nodeEvent():
		return "node " + e.NodeName
	default:
		return "test"
	}
}

// ---- The events, each with its sentence ----
//
// The sentences are in the voice of the console's [panel] lines and the
// drill-in's notes: plain, specific, lower-case where the console is, no
// exclamation. The object's name leads where the phone's title does not
// already carry it in the same breath.

// NodeOffline is a node the Panel lost its connection to.
func NodeOffline(id, name string, at time.Time) Event {
	return Event{
		Class: push.ClassAttend, Name: push.EventNodeOffline, NodeID: id, NodeName: name, At: at,
		Body: "node " + name + " went offline — the panel lost its connection to the agent",
	}
}

// NodePartial is a node whose Agent answers but cannot reach its container
// runtime.
func NodePartial(id, name string, at time.Time) Event {
	return Event{
		Class: push.ClassAttend, Name: push.EventNodePartial, NodeID: id, NodeName: name, At: at,
		Body: "node " + name + " can't reach Docker",
	}
}

// ServerCrashed is a server that stopped without an operator asking it to.
// The exit code goes in when the Agent saw one, the way the drill-in shows it.
func ServerCrashed(sv ServerRef, exitCode int64, exitKnown bool, at time.Time) Event {
	body := sv.Name + " stopped unexpectedly"
	if exitKnown {
		body += " — " + ExplainExit(exitCode)
	}
	return Event{Class: push.ClassAttend, Name: push.EventServerCrashed, Server: sv, NodeID: sv.NodeID, Body: body, At: at}
}

// WatchdogRestart is the watchdog bringing a crashed server back on its own.
func WatchdogRestart(sv ServerRef, at time.Time) Event {
	return Event{
		Class: push.ClassHealed, Name: push.EventWatchdogRestart, Server: sv, NodeID: sv.NodeID, At: at,
		Body: "the watchdog restarted " + sv.Name,
	}
}

// CrashLoop is the escalation: restarts inside one hour, n of them.
func CrashLoop(sv ServerRef, n int, at time.Time) Event {
	return Event{
		Class: push.ClassAttend, Name: push.EventCrashLoop, Server: sv, NodeID: sv.NodeID, At: at,
		Body: fmt.Sprintf("%s is crash-looping: the watchdog has restarted it %d times in the last hour", sv.Name, n),
	}
}

// BackupKind says which backup failed, because the sentence names it: a
// scheduled one nobody was watching is a different worry from one an operator
// just asked for.
type BackupKind int

const (
	// BackupManual is one an operator started from the console or the API.
	BackupManual BackupKind = iota
	// BackupScheduled is one a schedule started.
	BackupScheduled
	// BackupFinal is a retire's final backup; its failure abandons the retire.
	BackupFinal
)

// BackupFailed is a backup the node reported FAILED, with the node's reason.
func BackupFailed(sv ServerRef, kind BackupKind, reason string, at time.Time) Event {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the node gave no reason"
	}
	var body string
	switch kind {
	case BackupScheduled:
		body = "the scheduled backup of " + sv.Name + " failed: " + reason
	case BackupFinal:
		body = "the final backup of " + sv.Name + " failed, so its retire was abandoned: " + reason
	default:
		body = "the backup of " + sv.Name + " failed: " + reason
	}
	return Event{Class: push.ClassAttend, Name: push.EventBackupFailed, Server: sv, NodeID: sv.NodeID, Body: body, At: at}
}

// PlayersJoined is one or more named players joining, with the count online
// after them.
func PlayersJoined(sv ServerRef, names []string, online int32, at time.Time) Event {
	return Event{
		Class: push.ClassAlive, Name: push.EventPlayerJoined, Server: sv, NodeID: sv.NodeID, At: at,
		Body: fmt.Sprintf("%s joined %s · %d online", joinNames(names), sv.Name, online),
	}
}

// PlayersJoinedCount is a count-only spec's join: the game says how many are
// on, not who. joined is how many the count rose by.
func PlayersJoinedCount(sv ServerRef, joined int, online int32, at time.Time) Event {
	who := "a player joined"
	if joined > 1 {
		who = fmt.Sprintf("%d players joined", joined)
	}
	return Event{
		Class: push.ClassAlive, Name: push.EventPlayerJoined, Server: sv, NodeID: sv.NodeID, At: at,
		Body: fmt.Sprintf("%s · %d online", who, online),
	}
}

// Test is what POST /devices/{id}/test sends.
func Test(panelHost string, at time.Time) Event {
	return Event{Class: push.ClassAttend, Name: push.EventTest, Body: "test alert from " + panelHost, At: at}
}

// maxNamedPlayers is how many names one alert lists before it counts the rest.
// A lock screen shows a line or two.
const maxNamedPlayers = 3

// joinNames lists names the way a sentence does: "a", "a and b", "a, b and c",
// and "a, b, c and 2 others" past maxNamedPlayers.
func joinNames(names []string) string {
	switch n := len(names); {
	case n == 0:
		return "a player"
	case n == 1:
		return names[0]
	case n <= maxNamedPlayers:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	default:
		rest := n - maxNamedPlayers
		other := "others"
		if rest == 1 {
			other = "other"
		}
		return strings.Join(names[:maxNamedPlayers], ", ") + fmt.Sprintf(" and %d %s", rest, other)
	}
}
