---
title: Share links
description: A read-only public card for one server — how to make a share link, what it shows and never shows, how it unfurls in Discord and what the page behind it looks like, where the join address and password come from, revoking and expiry, game art on a LAN-only Panel, getting past Cloudflare, and the audit entries.
section: operate
order: 39
---

A **share link** is a URL you paste into a Discord channel (or any chat that
previews links) so friends can see a server's card: the game, its status, how
many people are on, and, if you choose, how to join. It needs no account and
gives no access to the Panel. It is the public counterpart of
[sharing a server with a user](/wiki/operate/users/#sharing-a-server), which
lets another *account* reach the server.

## Making one

Open the server's drill-in. The **share links** block sits under **access** and
is shown to anyone whose role holds `server.config` on that server: the owner,
administrators, and an Operator it is shared with.

**New share link** opens a short form:

- **label**: your own name for the link (`pinned in #general`), at most 60
  characters. Nobody else sees it; it is how you tell links apart.
- **show the address and port**, or **show the invite code** for a game that
  joins by code. Off by default.
- **show the join password**. Off by default, and see
  [revoking](#revoking-and-expiry) before you turn it on.
- **expires**: never, or in 1, 7 or 30 days. A link with no expiry lives until
  you revoke it, which is what a pinned message wants.

**Create link** shows the URL **once**, in the same violet-edged box the SFTP
password uses, with a copy control. On a Panel served over plain HTTP the browser gives the
page no clipboard, so **copy** selects the URL for you to copy by hand instead.
The Panel stores only a digest of the
link's token, so once you close the box or move to another server, the URL
cannot be shown again. If you lose it, revoke the link and make another.

A link needs the Panel's **public address** (*sessions & security*, or
`KRAKEN_PUBLIC_URL`; see [the public address](/wiki/configure/email/#the-public-address)).
Without one the block says so instead of offering the form, because a link
built from whatever address a request arrived on could point anywhere. A server
holds at most 25 live links (expired ones do not count); revoke one to make room.

## What a link shows

Every link shows:

- the server's display name and the game's name, over the game's art;
- a status, with the colour Discord paints the embed's side bar;
- the player count, while the server is running and the count is known;
- the time of the reading, in UTC.

| status | side bar | when |
| --- | --- | --- |
| **Running** | sodium gold | the server is running |
| **Starting** | sand | starting, installing, or restoring a backup |
| **Stopped** | sand | anything else: stopped, crashed, failed install, retired |
| **Unreachable** | violet | the server's node is not online, so the Panel cannot vouch for any state; no player count is shown |

A link shows the address (or invite code) and the join password **only** when
you switched them on for that link, and only the values the game's spec names in
its [`join` block](/wiki/specs/writing/#the-join-block).

**A link never shows** anything else about the server or the Panel: not the
server's id, the node's name or LAN address, the owner, other settings or
variables (admin and RCON passwords included), SFTP details, logs or errors.
The page is built from an allowlist of the fields above, not from the server
record with fields removed.

## In Discord

Paste the URL into a channel and Discord fetches it and draws an embed:

- **the title** is the server's name, as a link to the share page;
- **the text under it** is the game, status and player count, then the join
  values on a second line. This is the one place in Discord a player can select
  and copy them from;
- **the image** is a 1200×630 card the Panel draws itself: the game's art, the
  game in large type, the server's name above it, a status chip, the readings
  and the time they were taken.

**A Discord embed is a snapshot.** Discord fetches the link when it is posted
and keeps what it drew; the card in an old message does not update when the
server starts or stops. Discord also caches each URL for about half an hour, so
posting the same link again soon after can show the earlier card. The time on
the card is there so nobody mistakes an old card for a live one. For a status
that updates, see [what is not here](#what-is-not-here).

## The page behind the link

Tapping the link opens a page laid out for a phone: the art, the server and
game, the status, and a **Join** panel with a **copy** control for each value and
**copy all**, which copies every value on its own line, ready to paste into a
direct message. The password is masked until **show** is pressed, and copying it
does not need it shown. Without JavaScript the page still shows every value as
plain text.

The page is never indexed by search engines, and opening it never uses the link
up: Discord fetches it several times, and every friend who taps it fetches it
again. The block's row shows when a link was last opened.

## Where the join values come from

The spec decides how a player joins, so the share form names the right thing:

- **By address** (most games): the Panel builds `host:port` from the server's
  [DNS record](/wiki/configure/network/#game-ports-end-to-end) when it has one, else the node's
  detected WAN address, else its public host, and always adds the server's
  allocated game port. A private, loopback or link-local address, or a bare
  machine name, is never shown; if that is all the node has, the link shows no
  address rather than your LAN.
- **By invite code** (Windrose): the value of the setting the spec names. A
  Windrose server whose invite code is left blank uses a code it generated
  itself, which the Panel does not know, so its link shows the password only.
  Set the code in the server's settings to share it.
- **The join password** is the setting or variable the spec names as the one a
  player types. A spec cannot name an admin or RCON password there; the
  validator refuses it. For Enshrouded it is the **Friend** group's password.

A spec without a `join` block shares status and players only, and the form says
so.

## Revoking and expiry

**Revoke** on a link's row closes its page and its card at once: both answer the
same plain "link not available" page, with nothing for a chat to draw, whether
the link was revoked, expired or never existed.

:::warning
**Revoking does not reach into Discord.** A card already posted stays in the
message it was posted in, text and image, including any join values it showed.
If a link showed the password, revoking the link is not enough to take the
password back: **change the password** in the server's settings. The form says
this when you turn the password on.
:::

An expired link behaves like a revoked one. Revoked and expired links leave the
list and are deleted from the database 30 days later; the audit log keeps the
record.

## Game art and a LAN-only Panel

The card's art is the spec's `banner_url`, which the Panel downloads when it
draws a card. Because an anonymous request can cause that download, it is held
tight: **https** only, to **public addresses** only (checked after DNS, so a
name that resolves to your LAN is refused), at most three https redirects,
4 seconds, 12 MB. The art is kept for six hours.

A Panel with no outbound internet simply draws the card on the plain water
column. The failed download is remembered for ten minutes, so it costs the wait
once rather than on every card.

## Behind Cloudflare

Discord's fetcher has to reach `/s/…` on your public address. Two things in
front of the Panel can stop it, and both fail silently, as an embed that never
appears:

- **Custom WAF rules**, such as a rule that blocks every country but one. A
  rule like that also blocks friends abroad from the share page. Add a **Skip**
  rule above it for `http.request.uri.path wildcard "/s/*"`.
- **Bot Fight Mode** on the Free plan. It **cannot be skipped** by any WAF rule
  or Page Rule. If it challenges Discord's fetcher, the choices are turning it
  off or a paid plan, where Super Bot Fight Mode can be skipped.

To see what happened to Discord's fetch, look in the zone's HTTP request log for
user agents containing `Discordbot` and read the response status and security
action. A `200` with no action means Cloudflare let it through and anything
missing is on the page itself.

## Rate limit

The share page and its card are limited per client address: 120 requests a
minute with a burst of 40, generous because one share is several fetches from
Discord's few addresses and a channel of friends can share one NAT. It belongs
to the token-redemption group, so it is on when `KRAKEN_RATE_LIMITS` is `all` or
`downloads`; see [limits and logging](/wiki/configure/limits-and-logging/).

## In the audit log

| action | actor | meaning |
| --- | --- | --- |
| `server.share:<link id>` | whoever made it | a link was made; `+address` and `+password` are appended when it shows them |
| `server.unshare:<link id>` | whoever revoked it | the link was revoked |

The link's token never reaches the log. Opening a link is not audited; the
block's *opened* time records it instead.

## What is not here

- **A status that updates in Discord.** An embed is a snapshot by design. A bot
  that edits one pinned message as the server changes is tracked in
  [#207](https://github.com/briggleman/kraken/issues/207).
- **Player names.** A link shows how many are on, never who.
- **A platform mark.** The card and page do not yet say whether the server runs
  on Linux or Windows.
