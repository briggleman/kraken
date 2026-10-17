---
title: Users, roles and invites
description: The four built-in roles and what each may do, the two ways to create an account — a one-time password shown once, or an emailed invite that lets the person choose their own — what the invitee sees, the 24-hour single-use rules with resend and revoke, what invite.sent, invite.accepted and the rest mean in the audit log, and how to recover from a failed send.
section: operate
order: 38
---

Kraken is usually one operator and their own fleet, and that is the default
install: the bootstrap admin is the only account. When other people need in —
a friend who runs one of the servers, somebody who should only read the
dashboard — they get accounts of their own, with a role that says what they
may do, and a way to set a password that never passes through you.

Everything on this page lives in the **users** sheet, reached from console
settings › *users & access* › **manage users**. It needs the `user.manage`
permission, which Owner and Admin hold.

## The four roles

A user has exactly one role. Roles are sets of permissions; the built-in four
cannot be edited yet (a role editor is tracked as #125).

| role | may |
| --- | --- |
| **Owner** | everything, including settings, nodes, users and deletes |
| **Admin** | everything about servers, specs, nodes, backups and settings; manage users; read the audit log. Cannot act on owners |
| **Operator** | start, stop, restart, kill; console and command; files; backups; create servers; read specs and nodes. No settings, no users |
| **Read-only** | read the dashboard, the console and the specs; act on nothing |

Operators and read-only users are scoped to the servers they created unless a
role grants `server.any`, which Owner and Admin have through their wildcards.
The exact permission list is in
[`internal/panel/rbac/rbac.go`](https://github.com/briggleman/kraken/blob/main/internal/panel/rbac/rbac.go),
and the users sheet's legend restates it in a sentence each.

## Creating an account

The create form at the bottom of the sheet has two modes.

**A one-time password, shown once.** The default, and the only mode until
outbound email is configured. Your browser mints a strong random password,
the account is created with it, and the password is shown to you exactly once.
Copy it and hand it to the person however you hand people secrets; the Panel
never shows it again. The **reset** action on a row mints a new one the same
way.

**An emailed invite.** Once a relay and a public address are configured
([outbound email](/wiki/configure/email/)) and you have typed an email
address, a toggle appears — *send an invite instead of a one-time password*,
on by default — and the button reads **send invite**. The account is created
with a password nobody knows, and the person is emailed a link that lets them
choose their own. Nobody, you included, ever sees a password for the account
before they do.

The invite email names you (the account that sent it), the username, the
role, the Panel's public host, and when the link stops working. It is sent
as text and as HTML, from the address configured as the relay's sender.

## What the invitee sees

The link opens the Panel on a sheet of its own: *Welcome aboard, captain* —
the panel, the role, the time left — and two password fields with the same
twelve-character floor and strength meter the first-run rotation uses. The
token leaves the address bar the moment the page loads, so it does not sit in
the browser history. Setting the password signs them straight in: the Panel
answers with a session the way login does, and they land on the pane.

A link that is no longer good — used, expired, revoked, or replaced by a newer
one — opens one message: *this invite link is not valid any more … ask the
person who invited you to send a new link.* Every reason reads the same, so a
wrong link teaches nothing about the account behind it.

## The rules

- A link is good for **24 hours** from when it was sent.
- It works **once**. Accepting it consumes it; a second use of the same link
  is refused.
- An account has **one open invite at a time**. **Resend** mints a new link
  and emails it; the old one stops working the moment the new one is sent.
  One resend per account per minute, so a double-click does not send the
  same person two emails.
- **Revoke** kills the open link without touching the account. The account
  stays, with a password nobody knows, until you resend or reset.
- Disabling the account revokes its invite; deleting the account deletes it.
- Until the invite is accepted, a sign-in attempt for the account gets the
  ordinary "that username and password do not match" — the account is not
  described any differently from any other.
- Accepting the invite **ends every other session** the account had. There
  should not be any; if there were, they do not survive the person proving
  the mailbox.
- The link is stored only as a digest, like a session. A copy of the
  database holds nothing anyone could redeem.

The users sheet shows an open invite as a chip on the row — `invited · 23h
left`, `invite expired`, or `invite failed` — with **resend** and **revoke**
beside it. The chip disappears when the invite is accepted.

## When the email does not go out

If the relay refuses the message after the account has been created, the
sheet says so, the account stays, and the row shows `invite failed` with a
**resend** action. Fix whatever the relay said (the sentence is the relay's
own, passed through) under [outbound email](/wiki/configure/email/), send a
test message to yourself, and then **resend**. Nothing about the account needs
to be redone.

An account whose invite has **expired** is the same story without the error:
**resend** mints a fresh 24 hours.

## In the audit log

| action | actor | meaning |
| --- | --- | --- |
| `invite.sent:<username>` | the admin | an account was created with an invite; status 201, or 424 when the relay refused |
| `invite.resent:<username>` | the admin | a new link was sent; status 200, or 424 |
| `invite.revoked:<username>` | the admin | the open link was killed |
| `invite.accepted` | **the new user** | the password was set and the person signed in — their first row in the log |
| `/invites/{token} — rate limited (…)` | anonymous | too many link lookups from one address; one row per address per window, never one per request |

The path recorded for an invite route is the route pattern, never the path the
request arrived on: that path *is* the token, and a row that kept it would
hand a live link to anyone who can read the log.

## What is not here

- **Self-service password reset by email.** Deliberately. The threat model of
  "anyone who can type an email address can make the Panel send mail about
  it" is a different one from "an administrator decided to invite someone",
  and it needs its own thinking about enumeration and timing. An operator
  who has lost their password asks an admin to **reset** it, or an admin who
  has lost theirs uses the bootstrap path described under
  [installing the Panel](/wiki/install/panel/).
- **Changing an email address with re-verification.** Changing the address on
  a row is an edit, nothing more.
