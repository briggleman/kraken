---
title: Troubleshooting
description: Real incidents as symptom, cause and fix — the SteamCMD state that repeats forever, the whole fleet going offline at once, a container the Panel lost track of, a removal a node still owes, and the Cloudflare 400 that is not about a certificate being invalid.
section: reference
order: 64
---

Failures that happened, each written the way you will meet it: the symptom
first, because that is all you have at the time.

## Every install ends with `state is 0x6` or `0x602`

**Symptom.** The pass ends with a line from the `state is 0x…` family

```text
Error! App '4019830' state is 0x6 after update job.
```

on every attempt, including a reinstall. Before 0.50.1 this was worse than a
failure: the Agent did not recognise the line, treated the pass as fine, and
started the server on the **old** build, so the symptom was a server that
refused to update and never said why.

There are two causes behind that one line, and they need different fixes. What
tells them apart is the run of `Update state` lines just above it.

**Cause 1 — a stale download state.** SteamCMD keeps
`steamapps/appmanifest_<appid>.acf` and a `steamapps/downloading/` directory
between runs, and when they disagree with what the depot now holds, every
subsequent `app_update` gives up in the same place. `0x6` is
`StateUpdateRequired | StateFullyInstalled` — SteamCMD holding both beliefs at
once and abandoning the job meant to settle them. Validating does not clear it,
because validation trusts the manifest, which is also why the failure repeats
exactly rather than intermittently.

You will usually see the whole exchange land inside a single second, having
asked for no bytes at all:

```text
16:02:29 Connecting anonymously to Steam Public...OK
16:02:29  Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)
16:02:29  Update state (0x0) unknown,       progress: 0.00 (0 / 0)
16:02:29 Error! App '4019830' state is 0x6 after update job.
```

**Cause 2 — an update that downloaded but could not be committed.** Steam
replaces a file by writing the new copy beside it as `<name>~RF<hex>.TMP` and
then renaming that over the original. If the rename fails, the staged `.TMP`
stays and **the original is already gone**. Every later pass re-stages the same
file and fails the same rename, so this repeats exactly too.

Here the verify runs to completion and there is no download state anywhere,
because the content is already on disk:

```text
 Update state (0x3)  reconfiguring,      progress:  0.00 (0 / 0)
 Update state (0x5)  verifying install,  progress: 80.32 (3972809953 / 4946331918)
 Update state (0x81) verifying update,   progress: 97.47 (4821282414 / 4946331918)
 Update state (0x0)  unknown,            progress:  0.00 (0 / 0)
Error! App '4019830' state is 0x602 after update job.
```

`0x602` is `StateUpdateStarted | StateUpdatePaused | StateUpdateRequired`:
started, stopped before finishing, still owed. A missing `0x61 downloading`
state is the thing to notice — nothing needed fetching, so the failure is at the
commit.

**Which one you have.** Open the server's **Files** tab and look in the
install tree for a file ending `~RF<hex>.TMP`.

- No such file: cause 1.
- A `.TMP` **and** the file it is named after: an ordinary in-flight artifact.
- A `.TMP` with **no** matching file: cause 2, and the missing file is the one
  the update was replacing. If it is the server binary, the server could not
  have started either way.

**Fix for cause 1.** Delete both, then run the install again.

1. Open the server's **Files** tab, or connect over SFTP.
2. Delete `steamapps/appmanifest_<appid>.acf`.
3. Delete the `steamapps/downloading/` directory.
4. Start the server, or reinstall if it is in `install_failed`.

**Fix for cause 2.** The Agent now does this itself. After a failure from the
`state is 0x…` family it scans the data dir for `~RF<hex>.TMP` files, deletes
every one whose target is missing, and runs the pass once more — provided the
time left before the Panel's 30-minute deadline covers a second pass (taken as
1.25× the first) plus two minutes for the config and start that follow it. The
install console names each file it removed and the file that was missing, and
after a retry that works it says so again as its last line, so the record
survives a long second pass:

```text
[kraken] removed orphaned SteamCMD staging file RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP — its target RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe is missing, the mark of an update that could not be committed
[kraken] retrying the install pass once, now that 1 orphaned staging file(s) are cleared
…
[kraken] recovered: removed 1 orphaned staging file(s) before this pass: RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP
```

A `.TMP` whose target is present is reported as `in-flight … — not touched` and
left alone. There is no retry when nothing was removed, when any orphan is
still locked (whatever holds it would fail the second pass the same way), when
the install used a one-time Steam Guard code (it cannot be replayed), or when
the time left is too short. Then, or when the retry fails too, the pass lands
in `install_failed` and `last_error` says what was found, with the state
decoded, e.g.
`state is 0x602 (update started, update paused, update required)`. If the
orphans were cleared but no retry ran, reinstall and the next pass starts
clean. `steamapps/appmanifest_<appid>.acf` is never touched automatically.

On an Agent older than this, do it by hand: delete the orphaned `.TMP` and
reinstall — not start, since the file it was replacing is not there to run.

If the delete itself fails — the Files tab says the file `is in use by another
process`, or passes on the node's `Access is denied.`, or the Agent's message
says the file `is still locked — a container may be holding it` — that is the
same lock that stopped the rename, still held. On a Windows node it is usually a game
container that is still running: stop it, then delete the `.TMP` and reinstall.
Before starting the server, check the Files tab again and confirm the real file
is back with no `~RF…` suffix — the install reporting success is not the same
thing.

A running container on the data dir is how the tree got into this state in the
first place, so the Agent now checks before every pass. It refuses to run the
install while any container that carries the server's label, or binds its data
dir (or a folder inside it, or a writable folder above it), is still running,
and says so by name: the pass fails with
`refused to run the install pass: container kraken_<id> (<short id>) is running…`
in `last_error` and in the install console. Nothing on disk is touched, so the
server is not marked `install_failed`: an update lands `offline` (its stop had
already run), and a reinstall stays in the stopped state it started from. Stop that
container — `docker ps` on the node shows it, and it may be one Kraken is not
tracking — then run the install again. A container that has merely exited is
removed for you, with a `[kraken]` line in the install console saying which.
The one exception to the refusal is the server's own install container,
`kraken_<id>_install`, left over from an earlier pass: it is removed in any
state, running included, because the pass is about to replace it. While a pass
runs, the Agent also refuses any start or restart of that server.

The saves are not in any of these; they are wherever the spec's backup globs
point. Since 0.50.1 the Agent treats the `state is 0x…` family as an install
failure, so a repeat lands in `install_failed` with the line as `last_error`
instead of quietly relaunching the old build.

### After a reinstall or update the node shows no container for the server

This is expected, not a second fault. Before a pass the Agent removes the
server's stopped game container (the check above), and the one-shot install
container is removed once the pass reports its verdict. A **reinstall** — and
the drill-in's **update**, which runs the same pass — stops at `offline`
rather than going on to start, so until you press **start** the node has no
container for that server at all: `docker ps -a` lists nothing under
`kraken_<id>`, and the node band does not count it as untracked or as a
removal owed. The install console says so in the closing line,
`[panel] install complete — <name> is ready to start; no container exists until you start it`,
and in the Agent's removal line when there was a container to remove,
`[kraken] removed exited container kraken_<id> (<short id>) … — start recreates it after this pass`.
**start** creates the container fresh from the current image.

Two other places state it without the install console. The drill-in's empty
console reads `installed · no container until start` for an offline server
whose node has no container for it in any state, and `no output — server is
dark` while a stopped container is still there. The node band's container line
counts stopped containers, `containers 2 running · 1 stopped`: an offline server
missing from that count has no container. Both read the Agent's own report of
every managed container with its state, which an Agent older than 0.59.0 does
not send — it reports only running containers, so the drill-in note and the
stopped count cannot appear until the Agent is updated.

The pass before the reinstall is not lost either. The install log keeps one
attempt back: the console shows a `previous attempt` row above the current
output, with when it started and how it ended, and `show` opens its lines in
place. That is where a failed update's cause is, when the reinstall is what
cleared it.

## Every node goes offline at once

**Symptom.** The whole fleet reads `offline` at the same moment, which is not
how real outages usually arrive. The Panel is up and serving the UI. A node's
`agent.log` repeats a dial failure against the Panel's tunnel port:

```text
dial tcp <panel-address>:9443: connectex: No connection could be made because
the target machine actively refused it.
```

**Cause.** The Panel's `9443` is not published or not reachable. The usual way
to get there is hardening: binding the Panel's HTTP port to loopback so a
reverse proxy can front it, and binding the tunnel listener the same way while
you are in the file. Tunnel mode is the default for new nodes, so every tunnel
node loses the Panel at once.

Enrollment succeeding proves nothing about this. Enrollment is an outbound HTTP
call to the Panel's HTTP port; the tunnel is a separate connection to `9443`.

**Fix.** Publish `9443` again and make sure it is reachable from where your
Agents are.

- In Compose, check that the port is in the Panel service's `ports` (or that the
  service is on host networking) and that nothing bound it to `127.0.0.1`.
- Raw mTLS cannot be proxied, so `9443` cannot live behind Nginx, Caddy or a
  Cloudflare Tunnel the way the HTTP port can. Give it its own address or DNS
  name.
- Confirm from a node, not from the Panel host.

The nodes come back on their own once the listener is reachable; there is no
re-enrollment step. [Ports and firewall](/wiki/configure/network/) has the full
table of who is allowed to reach what.

## `containers N running · 1 untracked` that will not clear

Two different problems print this line. Hover the badge: it names the server id,
and whether the Panel has a row for that id decides which one you have.

### A row exists: the server reads `install_failed` or `offline`

**Symptom.** A node's band reads one more running container than the Panel has
`running` rows for, and it stays that way. The server in question shows
`install_failed`, or `offline`, while players are still connected to it.

**Cause.** A restart that failed at the *stop before update* step while the
Agent was unreachable, on a Panel older than the fix for
[#328](https://github.com/briggleman/kraken/issues/328). The 0.50.0 update pass
is stop, install, start. When the stop could not be delivered, the Panel
recorded the failure as `install_failed` even though nothing on the node had
changed: the container kept running throughout, and the Agent adopted it again
when it came back.

That is a state lie rather than a data problem. `install_failed` means "the
install tree is suspect", and a stop that never arrived says nothing about the
tree. It also locks start and restart behind a reinstall, which is the part you
feel.

**Fix.** Get the Agent back, and wait one reconcile pass. The Panel now believes
the Agent about its own containers: a managed container found running behind an
`offline` or `install_failed` row moves that row to `running` and clears the
stale error, so the badge clears itself with no reinstall. (On a Panel that
predates the fix, reinstall the server once the node is online.)

The pass no longer produces this state either. A stop that fails before the
install leaves the server exactly where it was — `running`, with the reason in
`last_error` — and a power action aimed at a node the Panel cannot reach is
refused with a `503` naming the node instead of starting a pass that cannot run.

### No row: a server you deleted is still running

**Symptom.** The badge names a container whose server id matches nothing in the
Panel. Players may still be connected to a server you deleted, the Agent log
shows `watchdog: adopted running server <id>` after every Agent restart, and the
container comes back after every crash. A new server can even appear to "have
the old server's backups": it does not — the listing is keyed by id — you are
looking at the old server, still alive on the node.

**Cause.** The server was deleted while its node could not be told, on a Panel
older than the fix for [#354](https://github.com/briggleman/kraken/issues/354).
The delete sent the removal once, ignored whether it arrived, and deleted the
row regardless. The node, built to be self-sufficient, then did exactly what it
is meant to: its watchdog adopts running containers carrying the
`kraken.server_id` label at startup, and its persisted spec gave it everything
it needed to keep restarting one.

**Fix.** Press **retire** beside the container's name on the node band (see
[Retiring an untracked container](/wiki/operate/fleet/)). The node stops and
removes the container, the Agent forgets its spec, and nothing of the server's
data or backups is touched — clear those by hand, on the host and the backup
target, if you no longer want them. From the API:

```sh
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  https://panel.example.com/api/v1/nodes/<node-id>/containers/<server-id>
```

It is no longer produced either. A delete that cannot reach the node is now
remembered on the node as a pending removal and finished when the node answers;
the band reads `removals · N pending` in the meantime.

:::note
On an Agent older than 0.54.0, a transient `1 untracked` during an install or
update pass is **normal**. The one-shot install container carries the same
managed label the game container does, so while it runs the node is running one
more than the Panel counts, and it clears when the pass ends. From Agent 0.54.0
the Agent names its containers and the Panel matches the install container to
the row it belongs to, so the badge no longer appears for a pass at all, and
when it does appear its tooltip names the container. Either way it is only the
badge that persists that means something.
:::

## `removals · N pending` that will not clear

The node answers but its Agent keeps failing the removal. Hover the line: the
last error is verbatim, and both kinds begin `docker: remove server <id>:`.
One that contains **`delete its data`** means the containers are gone but the
data directory could not be deleted, usually a file held open on a Windows
host. Anything else means Docker would not remove the container — a daemon
restarting underneath it, or on Windows a container still being torn down. The
Panel keeps retrying either way, backing off to an hour apart, and logs a
warning only when the reason changes; once the cause is gone, the next retry
clears it.

**A removal that will never land can be dismissed** — a node that is gone for
good, or a container you already cleared on the host:

```sh
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  https://panel.example.com/api/v1/nodes/<node-id>/removals/<server-id>
```

It needs `server.delete` and `node.manage`. Nothing is sent to the node, and the
memory and ports the removal was holding are released — so if the container is
in fact still running there, retire it or remove it on the host, or those ports
can be handed to a new server while it still holds them. Deleting the node from
the Panel drops its pending removals with the node record.

## Cloudflare returns 400 `The SSL certificate error`

**Symptom.** With Authenticated Origin Pulls enabled, every request through
Cloudflare comes back `400` with a body reading *The SSL certificate error*. The
proxy's error log is empty, which makes it look like the requests are not
arriving at all.

**Cause.** The wrong certificate authority is installed on the origin. Two
different objects arrive from the Cloudflare dashboard as a `.pem`:

- an **Origin Certificate**, which is a server certificate for your origin, and
- the **public Origin Pull CA**, which is what signs the client certificate
  Cloudflare presents.

`ssl_client_certificate` wants the second one. With the first one in that slot,
nginx cannot verify Cloudflare's client certificate and refuses the connection.
The empty error log is the other half of the trap: nginx records a client
certificate verify failure at **info** level, so a proxy logging at error, which
Nginx Proxy Manager does by default, shows nothing at all.

**Fix.** Read the 400's body rather than the log, then replace the file with
Cloudflare's public Origin Pull CA from
`https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem`
and reload the proxy. The exact configuration block is on [behind a reverse
proxy](/wiki/configure/reverse-proxy/).

## When it is none of these

Three places to look, in this order:

1. **The install log.** It is kept after the install finishes, success included,
   until the server is deleted or reinstalled. It does not survive a Panel
   restart, and the API says `retained: false` when it is gone rather than
   showing you an empty pane.
2. **The audit log.** Filter to failures. A screen of 4xx is somebody getting a
   request wrong; a screen of 5xx is the Panel or a node.
3. **`agent.log` on the node**, JSON, rotated at 10 MiB, in the Agent's state
   directory. On Windows, `restart-helper.log` beside it is what a self-update's
   relaunch wrote, step by step.

And one thing to check before anything else if the whole UI looks wrong: the
header's `stale` mark. A stale fleet view is a browser that cannot reach the
Panel, not a fleet that stopped working. Servers keep running and schedules keep
firing while your tab is out of date.
