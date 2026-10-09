---
title: Push alerts
description: What the iOS companion is told about your fleet and when — the three alert classes, every event and the sentence it sends, the crash-loop escalation and its quiet hour, why a node has to stay down for a minute before it pages, how devices are registered and revoked, the test alert, and what push.sent, push.failed and push.dropped mean in the audit log.
section: operate
order: 37
---

The Kraken iOS companion can tell you when something in the fleet needs you: a
node dropped off, a server crashed, a backup failed. The Panel decides what is
worth an alert and who may see it; the phone decides how loudly to say it.

Push alerts are **off until you configure a relay**. Apple only delivers
notifications sent with a signing key, and a self-hosted, GPL Panel cannot ship
one, so the Panel hands each alert to the **Kraken push relay**, which holds the
key and forwards to Apple. It is the one part of Kraken that talks to a service
outside your network. A Panel without a relay works exactly as before and loses
alerts and nothing else.

The relay never reads an alert. Each one is sealed on the Panel to the phone's
own key, and only the phone can open it: the relay sees the phone's push token,
a blob of ciphertext and when it was sent — not which server, not what
happened, not who you are. [Security](/wiki/security/) has the full model.

## Turning them on

Set two variables on the Panel and restart it:

```sh
KRAKEN_PUSH_RELAY_URL=https://push.example.com   # the relay's base URL; unset = off
KRAKEN_PUSH_RELAY_TOKEN=…                        # only if your relay asks for one
```

Both are in the [Panel configuration reference](/wiki/configure/panel/#push-alerts).
The Panel logs one line at startup saying whether alerts are on and which relay
host it will use — never the token. If a token is set and the URL is plain
`http://` to a host that is not this machine or a private address, it also warns
that the token would cross the network in the clear; use `https://`.

Then sign in on the companion app and allow notifications. The app registers
the phone with the Panel by itself, and keeps the registration fresh every time
it starts.

## What you are told

Every alert has a **class**, which the phone turns into how it interrupts you:

| class | how the phone says it | means |
| --- | --- | --- |
| **attend** | time-sensitive | something needs a human |
| **healed** | an ordinary notification | the fleet fixed itself; if these repeat, look |
| **alive** | quiet, grouped per server | someone is playing |

Each class can be turned off per phone (in the app, which calls
`PATCH /api/v1/devices/<id>/rules`), and one server's player alerts can be
muted without muting its crashes.

The events, with the sentence each one sends:

| event | class | the sentence | what to do |
| --- | --- | --- | --- |
| a node went offline | attend | `node abyss-win went offline — the panel lost its connection to the agent` | check the host is up and the Agent is running; a tunnel-mode node may simply have lost its network |
| a node can't reach its runtime | attend | `node abyss-lnx can't reach Docker` | start Docker (or Docker Desktop) on that host; the node reads `partial` until it can |
| a server stopped unexpectedly | attend | `dragonwilds-01 stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing` | open the server: the exit code is explained the same way under [Reading a crash](/wiki/operate/servers/#reading-a-crash) |
| the watchdog restarted a server | healed | `the watchdog restarted palworld-01` | nothing, once. Several in an hour becomes the next row |
| a server is crash-looping | attend | `palworld-01 is crash-looping: the watchdog has restarted it 3 times in the last hour` | read its console and install log; the game is failing on its own and restarting will not fix it |
| a backup failed | attend | `the scheduled backup of valheim-01 failed: no space left on device` (or `the backup of …` for one you started) | the node's reason is in the sentence; free the space, fix the mirror, or check the backup globs |
| a retire's final backup failed | attend | `the final backup of valheim-01 failed, so its retire was abandoned: …` | the server is back where it was and nothing was removed; fix the reason and retire again |
| a player joined | alive | `Kestrel joined palworld-01 · 4 online` | nothing — it is the good kind |

A server whose game only reports how many players are on, not who, says
`a player joined · 4 online`. A server sends at most one player alert a minute:
players who arrive together are folded into one sentence
(`Kestrel and Wren joined palworld-01 · 5 online`), and if the server stops
inside that minute the held joins are dropped rather than announced after its
crash. Players reconnecting after a crash or a restart count as joining.

## The crash-loop escalation

The watchdog restarting a crashed server is good news once and bad news three
times. So the restart that makes **three in the last hour** for one server is
sent as `attend`, "crash-looping", instead of a third "the watchdog restarted".

After that, the server goes **quiet for an hour**: further restarts send
nothing, because the crash-loop alert already said it, and "the fleet fixed
itself" right after "it is crash-looping" would contradict it. When the hour is
up, a restart is reported again — as healed, or as a new crash-loop if it is
still looping.

A restart that fails, leaving the server crashed, is reported as the crash, not
as a heal.

## Node alerts wait a minute

Most of the time a node that drops off is back before you could have done
anything: an Agent restarting into an update, a network blip, Docker Desktop
restarting. So a node has to **stay** offline or `partial` for **60 seconds**
before its alert is sent; if it is back by then, nothing is sent at all. A node
that flaps down and up inside that minute is reported only if it is down when
the minute is up.

**Agent updates do not page.** When you push an Agent update from the Nodes
page, the Agent restarts into the new build. For that node the Panel waits out
the update's own window — ten minutes — instead of one, so a slow Windows
service restart is not reported as an outage. A node still down when the window
closes is reported.

A Panel that has just started does not report the nodes it finds down on its
first look: a tunnel-mode node has not reconnected yet, and every upgrade would
otherwise page everyone.

## Devices

A phone belongs to the **user who signed in on it**, not to a session — it keeps
receiving alerts after the session it registered with expires.

- **Signing out of the app** revokes that phone.
- **Disabling a user** revokes every phone they registered, and **deleting a
  user** deletes them.
- **An administrator** (anyone with `user.manage`) can list another user's
  devices with `GET /api/v1/devices?user=<user id>` and remove one with
  `DELETE /api/v1/devices/<device id>?user=<user id>`.
- One phone signed into two accounts receives both accounts' alerts until it
  signs out of one. Neither account can take over or silence the other's
  registration.

Alerts follow **permissions at the moment they are sent**. A server's alerts go
to people who can see that server — its owner, or a role that reaches every
server — and node alerts to anyone whose role can view nodes. Take a server away
from someone, or change their role, and the very next alert reflects it; nothing
on their phone has to change. A disabled user gets nothing.

## The test alert

The app's "send a test alert" calls `POST /api/v1/devices/<id>/test`, which
sends `test alert from <your panel's host>` to that phone straight away and
waits for the relay's answer, for at most 20 seconds:

| answer | means |
| --- | --- |
| `sent` | the relay took it; if the phone shows nothing, look at its notification settings |
| `token_dead` | Apple no longer knows this phone's push token (the app was reinstalled, or notifications were reset). Open the app so it registers again |
| `rejected` | the relay refused the request; the reason says why |
| `dropped` | the relay did not answer in time |
| `disabled` | this Panel has no relay configured |

The test goes to the phone whatever its alert settings say, so it always tells
you whether the path works.

## When alerts do not arrive

**No alerts at all.** Check the Panel's startup line: `push alerts off` means
`KRAKEN_PUSH_RELAY_URL` is not set. Then send a test alert from the app. Then
look in the [audit log](/wiki/operate/audit/) for `push.` rows — if there are
none, nothing has happened that you are allowed to hear about (an Operator only
hears about the servers they own).

**One phone went silent.** Look up its device: if `token_invalid_at` is set,
the relay reported its push token dead and the Panel stopped sending to it.
Opening the app re-registers the phone and clears the mark.

**The audit log.** Every alert to every phone leaves one row, actor `system`,
target the device:

- `push.sent` — the relay accepted it.
- `push.failed` — the relay refused it and retrying would not help: a dead
  push token (`410`), or a request the relay will not take (`400`). The reason
  is in the row.
- `push.dropped` — the relay could not be reached, or kept answering busy, for
  two minutes. The Panel retries for that long (after 1, 3, 9 and 27 seconds,
  then every 30) and then gives up rather than deliver old news; the row says
  how many attempts it made and what the relay last said.

A run of `push.dropped` means the relay is down or unreachable from the Panel;
a `push.failed` with `410` is one phone that needs to open the app.
