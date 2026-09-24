---
title: Servers
description: Deploying a server from a spec and running it afterwards — the seven lifecycle states, what update-on-start does before every start, when it deliberately does not run, how to read a crash exit code, which settings wait for a restart, and what a delete removes.
section: operate
order: 31
---

A server is one game instance, running on one node, from one Game Spec. The spec
decides how it installs, launches, configures and backs up; you decide where it
lands, how much memory it reserves, and what its settings say.

## Deploying

Pick a spec, pick a node or let the scheduler pick, name it, deploy.

:::shot deploy
the deploy sheet on reef-01 with Valheim selected: the spec's variables, the memory field and the operations toggles. The node is chosen by which band's New Server you pressed.
:::

What the dialog is asking:

- **Memory.** Leave it blank to take the spec's own figure, which is its
  recommended memory falling back to its minimum. An explicit value has to be at
  least the spec's `min_memory_mb`; under that floor the game does not boot, so
  the Panel refuses rather than letting you find out at start time.
- **Node.** Optional. Pinning still goes through the scheduler, which checks
  eligibility and reserves the ports. An ineligible pin comes back as a `409`
  naming the reason, never as a quiet placement somewhere else.
- **Install BepInEx.** Shown only when the spec declares `bepinex_compatible`,
  and honoured only then. See [Mods](/wiki/operate/mods/).
- **Pin build.** Pins the server to the build this install pulls. Off by
  default; the section below is what it opts out of.
- **Steam Guard code.** Only for specs whose install needs an authenticated
  Steam login. Used for that one install and never persisted.

Ports come from the node's pool, 1:1 with the host. Deploying is asynchronous:
the server goes to `installing` and the install log streams into the console
pane while SteamCMD works.

## The seven states

| state | what is true |
| --- | --- |
| `installing` | the install script is running on the node |
| `install_failed` | the install script failed; `last_error` says why; start is refused until a reinstall |
| `offline` | installed and stopped, nothing holding the data directory |
| `starting` | the container is up, the readiness line has not appeared |
| `running` | the game is serving |
| `stopping` | a graceful stop is in progress |
| `crashed` | the process exited unexpectedly; the exit code is kept |

Four power actions drive it: `start`, `stop`, `restart` and `kill`. `kill` is
the one that does not ask the game nicely, and a world that saves on shutdown
will not have.

`install_failed`, `offline` and `crashed` are the stopped states, and they are
the three a reinstall accepts, because nothing holds the data directory in any
of them.

## Update on start

**Since 0.50.0, every operator-initiated start or restart re-runs the spec's
install script before launching.** For a SteamCMD title that is an
`app_update … validate` pass, which is how a server picks up a game update at
all. Before this, a healthy server stayed on the build SteamCMD pulled the day
it was created, and the only way forward was delete and recreate.

The sequence, once you press start and the pass runs:

1. The Panel stops the container if anything is holding the data directory,
   and the Agent confirms the container is really down before reporting the
   stop done.
2. The Agent checks nothing else still has the data directory: an exited
   container bound to it is removed, and a running one refuses the pass by
   name, since SteamCMD writing under a live game corrupts the tree. A refusal
   lands in `install_failed` like any other install failure, with the
   container named in `last_error`.
3. It runs the install script. The server reads `installing`, the response is a
   `202` carrying `updating: true`, and the install log streams to the console.
   A start that skips the pass (the opt-outs and the paths below) is the plain
   synchronous `200` instead.
4. It re-renders the config files over the fresh tree. **After** the update, not
   before, because the pass can restore a file the depot owns and your settings
   have to win.
5. It starts the game.

Where a failed pass leaves the server depends on how far it got, because the
three phases say different things about the install tree:

| phase that failed | where the server lands |
| --- | --- |
| the stop before the update (or reaching the Agent at all) | **back where it was** — `running` if it was running — with `last_error` set. Nothing on the node was touched, so there is nothing to reinstall; press start again once the node is back. |
| the install script | `install_failed` with `last_error` set. Start is refused until you reinstall — a half-written tree must not be launched over. |
| the start after a good install | `offline`. The tree is fine, the game did not come up, and a plain start retries it. |

The Panel also refuses the whole action up front when it has no live connection
to the node's Agent: a power action against an offline node answers `503` naming
the node, and the server's state is left alone. Before that, a restart aimed at
an unreachable node could mark a server `install_failed` over a stop that never
left the Panel, while the game went on serving players.

If a node's Agent reconnects and the Panel's row still disagrees, the reconciler
adopts what the Agent reports: a managed container found running behind an
`offline` or `install_failed` row moves that row to `running` and clears the
stale error.

### The two opt-outs

- **Per server: pin build.** The Config tab's toggle. A pinned server never runs
  the pass on start, and reinstall becomes its explicit "update now". An
  ordinary settings save cannot unpin a server by omission; the field has to be
  sent deliberately.
- **Per spec: `skip_update_on_start`.** The spec installs at create time and on
  an explicit reinstall only. It is also available per platform, for a spec
  whose install is idempotent on one platform and not the other, and a platform
  can only opt out, never opt a spec-wide opt-out back in. No bundled spec sets
  it. Setting it costs that game its updates, so I would treat it as a last
  resort rather than a convenience, and rewrite the script instead.

### What deliberately does not update

Four paths start a server without re-running anything, and each is a decision
rather than an omission.

- **Any start within 30 minutes of an install.** A create or reinstall that
  succeeds stamps the server's `provisioned_at`, and every start or restart
  inside that window skips the pass, which would only repeat an install that
  just ran. Starting does not clear the stamp, so a second start in the window
  skips it too. It
  covers the deploy form's "start once the install finishes" and an operator who
  stops to fill in settings first. It is a window rather than a "never started"
  flag so that a server created and left for days still updates on its first
  start. Editing a launch variable clears the stamp, because the install script
  may render it, even when the edit lands while the install is still running.
  A failed install clears it too. The settings response's
  `next_start_updates` says which way the next start will go.
- **Scheduled restarts.** A cron restart drives the Agent directly. A nightly
  restart is not an invitation to validate a 30 GB tree nightly. It runs on a
  server that is `running`, `starting` or `crashed`, so it still revives a server
  the watchdog gave up on. It is skipped on an `offline` one, because the Agent's
  restart is a stop then a start and would start a server someone had stopped,
  and on `stopping`, `installing` and `install_failed`. It is also refused while
  a required setting is empty. Any skip is recorded as the schedule's last error,
  shown on its row.
- **The crash watchdog's restarts.** The Agent restarts the container itself and
  never involves the Panel, so a crash loop cannot become a download loop.
- **A spec that needs a Steam login on a node with no stored credentials.** The
  pass would fail on the login, so it is skipped with a warning in the Panel log
  and the server starts on the build it has. Store the node's Steam credentials,
  or use reinstall with a Steam Guard code.

:::warning
The install script now runs against a fully installed data directory holding
live save games, so **it has to be idempotent**. `steamcmd … +app_update <id>
validate +quit` already is. A script that wipes the directory, re-seeds a config
the operator has since edited, or unconditionally re-downloads a large
unversioned artifact is not. If you write specs, [Writing a
spec](/wiki/specs/writing/) is the obligation in full.
:::

## Reinstall as "update now"

`POST /servers/{id}/reinstall`, or the action in the UI, re-runs the install
script once, immediately. It is both the retry for a failed install and the
explicit update for a server whose start does not update it.

It is accepted from `install_failed`, `offline` and `crashed`, and refused from
`installing`, `starting`, `running` and `stopping` with a `409` that names the
current state. It runs asynchronously, exactly like the create path: the server
flips to `installing` and the log streams.

Unlike the update-on-start pass, a reinstall *does* re-run the spec's BepInEx
overlay for a server deployed with it.

## The install log

The install runs on the node and is streamed to the Panel, which buffers it in
memory. There is no container left to tail once the phase ends, so the buffer is
the record.

- It keeps the last **500 lines** of the current attempt.
- It is kept **after the install finishes, success included**, because an
  installer can exit 0 having produced a broken tree.
- It is dropped when the server is deleted, and replaced when the next install
  starts.
- **It does not survive a Panel restart.** The API answers `retained: false`
  when nothing is held, which is not the same as an install that printed
  nothing, and the UI says which it is rather than showing you an empty pane.

## Reading a crash

When a server lands in `crashed`, the Agent captures the container's exit code
and the Panel keeps it until the next power action. The drill-in renders it as
`exit <n> / 0xXXXXXXXX`, and for the codes that come up repeatedly it adds what
they mean:

| code | what it usually is |
| --- | --- |
| `0xC0000135` | a DLL the game needs is missing from the container image |
| `0xC0000139` | a DLL is present but the wrong build; an export is missing |
| `0xC0000005` | access violation; the game crashed on a bad memory access |
| `0xC000007B` | bad image format, a 32/64-bit mismatch between the game and a DLL |

Exit `0` is called out as the process ending on its own without an error code,
which on a game server is more often a startup argument the game rejected than a
clean shutdown. A code between 129 and 164 is reported as the signal that killed
it, code minus 128, so `137` reads as signal 9.

Whether the watchdog restarts after a crash is the spec's
`startup.restart.on_crash`, with its own retry ceiling.

## Settings that wait for a restart

Two kinds of value, two different answers.

**Launch variables** are baked into the start command and the container
environment when the container is created, so they are never live. A change
takes effect on the next start, always.

**Settings** render into the game's own config files, which are written to the
bind-mounted data directory the moment you save. Whether the running game
notices is the game's business, and the spec declares it: `settings.hot_reload`
says the game re-reads its config while running. It changes nothing about what
gets pushed to the node, only what the Panel tells you afterwards.

The save response says which case you are in, so the UI can ask for a restart
only when one is actually needed: a running server, plus either a changed launch
variable or a settings change on a spec that does not hot-reload.

There is no per-setting "requires restart" flag. If you author specs, that is
worth knowing before you go looking for one.

## Deleting a server

Delete is in the drill-in, behind the typed confirmation. It removes the
server's containers and its data directory — the world and the rendered config —
on its node, releases the memory and ports it reserved, and deletes the record
together with its schedules.

**Backups are kept.** The archives are keyed by server id and stay wherever the
node's backup target keeps them — the node, a share, a mirror; the confirmation
says so. They are the part of a server most worth keeping, and a
retire-and-revive model that makes use of them is tracked in
[#360](https://github.com/briggleman/kraken/issues/360).

**A node that is down does not block a delete.** When the Panel cannot reach the
node, or its Agent reports that the removal failed, the delete still goes
through and the removal is remembered on the node. Until it lands the node keeps
the server's memory and ports allocated — the container may still be running and
bound — and the band reads `removals · 1 pending`. The Panel's node reconciler
retries it, backing off, until the node confirms; the allocation is released
then. [The fleet page](/wiki/operate/fleet/) has the details. If the Panel
cannot record the removal at all (its database is failing), the delete is
refused with a `500` and nothing is deleted.
