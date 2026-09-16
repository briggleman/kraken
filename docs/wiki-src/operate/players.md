---
title: Who is online
description: The live player count — the spec's query block and its three methods, where the count and the roster appear, and the difference between "nobody is aboard" and "nobody knows".
section: operate
order: 35
---

A spec's optional `query:` block tells the Agent how to ask a running game who
is on it. Without one, the readouts stay blank and say so. With one, the count
appears on the server card and in the drill-in, and for one of the three methods
the actual names do too.

## Three methods

| method | when to use it | yields |
| --- | --- | --- |
| `a2s` | the server answers Steam's A2S_INFO query. Most Source and Steam servers, Valheim among them. | count and cap |
| `palworld-rest` | Palworld's admin REST API. | count and cap |
| `log` | the game answers nothing but prints a line when a player arrives and another when one leaves. Early-access UE5 servers, typically. | count **and names** |

**`a2s`** sends an A2S_INFO packet over UDP to `127.0.0.1` on the server's
allocated host port, because game ports are published 1:1 and the Agent is on
the host. It handles the modern challenge handshake: a server that answers with
a challenge gets the query repeated with the challenge bytes appended. The
spec's `port:` for this method names a **spec port**, so Valheim names its
`query` port rather than its game port.

**`palworld-rest`** runs `curl` **inside the container** through a Docker exec,
against `http://127.0.0.1:<port>/v1/api/metrics` with basic auth. That is not a
workaround for anything: the REST port is deliberately not published on the
host, so the only place it answers is inside the container. The spec's `port:`
and `password:` for this method name **setting keys**, and the Panel resolves
the server's own values before handing them to the Agent.

**`log`** follows the container's console for the life of each run, matching a
join and a leave regex. Two details are worth knowing if you write one: the
leave pattern is tried first on every line, so a join pattern loose enough to
match a departure line cannot resurrect somebody who just left; and after an
Agent restart the run's log is replayed from its start, so who is aboard
survives the Agent going down. The regexes are RE2, and each must capture
`(?P<name>…)`, optionally with a `(?P<id>…)` the roster keys on so a renamed
character is one player rather than two.

## The cap

`a2s` and `palworld-rest` read the cap off the wire. A log never states one, so
`log` specs declare it: `max_players:` is the game's own constant, and
`max_players_setting:` names the setting key holding it when the operator picks
the number. The server's own value wins when it is present and parses above
zero; otherwise the constant. With neither, the readout shows a count and no
denominator.

## Where it shows

**On a server card**, as `n / max` over an occupancy rail, labelled `players
online`.

**In the drill-in header**, as the `players` stat, live from the stats
WebSocket while the drill-in is open and from the polled value otherwise.

**In the drill-in's roster pane**, headed `online · n`, listing names when the
spec uses `log` and saying plainly why it cannot when it does not: *names need
game query support* for a count-only method, *player names unavailable for this
game* for a spec with no query block.

## Unknown is a third answer

Nobody is aboard a server that is not running, and the count reads `0`. A
running server whose count has not arrived reads **unknown**, rendered as an em
dash with no rail on the card and `?` in the roster header. The two are stored
separately on purpose: "0 online" and "we do not know" are different facts and
the UI is not allowed to blur them.

The count-based methods are cached for 10 seconds, which is the sampling rate
you actually get. The `log` roster bypasses that cache entirely, because it is
maintained continuously from the console follower rather than sampled.

:::note
There is no fleet-wide "n online" total today. The count lives on each server's
card and in its drill-in.
:::

## Validation

The spec validator rejects an unknown method, a missing port for `a2s`, a
missing port or password for `palworld-rest`, and a `log` regex that does not
compile or captures neither group. Every one of those would otherwise fail
silently at runtime as "players unknown", which is the worst kind of wrong: a
readout that looks like an answer.
