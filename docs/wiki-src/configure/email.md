---
title: Outbound email
description: The SMTP relay the Panel sends through — what it is used for, the settings and which security mode to pick, the public address every emailed link is built from, provider recipes, and the SPF, DKIM and DMARC checklist that keeps an invite out of the spam folder.
section: configure
order: 26
---

The Panel can send email. It does so for exactly one thing today: inviting a
person to an account with a link that lets them choose their own password,
instead of an administrator handing one over. Nothing is sent until you
configure a relay, and a Panel without one loses that convenience and nothing
else.

Everything on this page is set in the UI under **console settings › email
(smtp)**, except the public address, which has an environment variable as well.

## What it is used for

- **Account invites.** Creating an account can send the new person a one-time
  link, good for 24 hours, that opens a set-password sheet on the Panel. The
  first password they choose is the only one that ever exists for the account.
  See [Users, roles and invites](/wiki/operate/users/).
- **The test message.** The *send test email* button in the same panel sends
  one message through the stored settings and shows you what the relay
  answered, so you know the route works before you need it.

That is the whole list. The Panel sends no digests, no alerts by email (alerts
go to [the phone](/wiki/operate/alerts/)), and nothing to anyone who was not
just invited by an administrator.

## The settings

| field | what it is |
| --- | --- |
| **relay host** | the SMTP server's hostname or address, with no scheme and no port |
| **port** | 587 for STARTTLS (the usual submission port), 465 for implicit TLS, 25 only for a relay on your own network |
| **security** | `starttls`, `tls` or `none` (see below) |
| **username** / **password** | the relay login. Leave the username empty for a relay that takes mail from your network without one. The password is stored encrypted and never shown again |
| **from address** | the bare address messages come from, such as `kraken@example.com`. It has to be one the relay lets you send as |
| **from name** | the display name beside it. Default `Kraken` |

The password is sealed with the Panel's secrets key before it reaches the
database, the same as the Cloudflare and UniFi credentials: see
[Security](/wiki/security/).

### Which security mode

- **`starttls`** opens a plain connection and upgrades it before a word of
  the message is said. The upgrade is **required**: a relay that does not offer
  STARTTLS is refused, never spoken to in the clear. This is the right choice
  for every provider on port 587.
- **`tls`** is encrypted from the first byte, usually on port 465. Pick it when
  the provider's instructions say "SSL" or name port 465.
- **`none`** talks in the clear. It exists for a relay on your own network, say
  a Postfix on the same host that already knows how to deliver. The UI says so
  in violet, because a login sent over it would travel in plaintext, and the
  Panel refuses to send one that way unless the relay is this very host.

The relay's certificate is always verified against the system roots. There is
no "skip verification" switch, on purpose: a mail relay with a bad certificate
is a relay you should not be giving a password to.

### Authentication

The Panel speaks `PLAIN`, `CRAM-MD5` and `LOGIN`, and picks the strongest one
the relay offers, in that order. Microsoft 365 and a number of appliances offer
only `LOGIN`, which is why it is there. OAuth-based mechanisms are not
supported; use an app password instead.

## The public address

A link in an email has to name the Panel, and the Panel has to be told its own
name. That is the **public address** under *sessions & security*, or the
`KRAKEN_PUBLIC_URL` environment variable, which wins when set and locks the
field. It is an absolute `https://` address, with an optional path prefix if
the Panel lives under one, and no query string.

:::note
**The Panel never builds a link from the address a request arrived on.** Behind
a proxy, the `Host` header is whatever the caller sent, and a password-setting
link built from it would point wherever an attacker chose. Set the public
address once and every emailed link comes from it. The UI will suggest the
address when your allowed WebSocket origins list holds exactly one entry.
:::

An `http://` address works, and the UI points out that the one-time token in an
invite link then travels in the clear.

## Provider recipes

Any provider's "SMTP settings for a mail client" page is what you want: the
Panel is a mail client as far as the relay is concerned.

| provider | host | port / security | login |
| --- | --- | --- | --- |
| Google Workspace / Gmail | `smtp.gmail.com` | 587 `starttls` | the address, and an **app password** (2-step verification has to be on) |
| Microsoft 365 | `smtp.office365.com` | 587 `starttls` | the mailbox, and an app password; *authenticated SMTP* has to be enabled for the mailbox |
| Fastmail | `smtp.fastmail.com` | 465 `tls` | the address, and an app password |
| Postmark | `smtp.postmarkapp.com` | 587 `starttls` | the server API token as both username and password |
| Amazon SES | `email-smtp.<region>.amazonaws.com` | 587 `starttls` | the SMTP credentials SES generates (not your IAM keys) |
| Mailgun | `smtp.mailgun.org` | 587 `starttls` | the domain's SMTP login |
| a Postfix on this host | `127.0.0.1` | 25 `none` | none |

The transactional services (Postmark, SES, Mailgun) are the easy path to a
sending domain that is properly set up, because they walk you through the DNS
records below as part of adding the domain.

## Deliverability

A homelab Panel's invite is a message from a domain nobody has heard of, and
the big providers will file it as spam unless the domain vouches for it. What
the From domain needs:

- [ ] **Send from a domain you control**, through a provider's submission port
  (587, STARTTLS). Do not run your own outbound MTA on a home connection:
  residential addresses are blocked on port 25 by most ISPs and have no
  reputation to send with.
- [ ] **SPF**: a `TXT` record on the From domain naming the provider's
  senders, `v=spf1 include:<the provider's spf record> -all` (or `~all`). Stay
  under SPF's limit of ten DNS lookups.
- [ ] **DKIM**: publish the provider's selector record,
  `<selector>._domainkey.<domain>`, and check that the provider signs with
  `d=` your From domain rather than its own.
- [ ] **DMARC**: a `TXT` at `_dmarc.<domain>` reading
  `v=DMARC1; p=none; rua=mailto:<a mailbox of yours>` to start, moved to
  `p=quarantine` once a few reports come back clean.
- [ ] **Alignment**: the header From domain, the envelope sender (the
  `Return-Path`) and the DKIM `d=` have to be the same organisational domain.
  A From of `kraken@home.example` relayed by a provider that signs as
  `provider.example` fails DMARC even with SPF and DKIM both passing.
- [ ] **A mailbox that exists** at the From address, or at least one that
  accepts bounces. People reply to invites.
- [ ] **Check it**: send a test message to a Gmail and an Outlook address and
  read `Authentication-Results:` in the raw headers. You want
  `spf=pass dkim=pass dmarc=pass`. mail-tester.com gives the same reading with
  a score.

What the Panel tells you: the test message's result line carries the relay's
final reply and the `Message-ID` it stamped, which is what you match against a
bounce or the relay's own log. DNS is the operator's side; the Panel does not
look it up for you, because a LAN-first Panel must not depend on outbound DNS
to say whether its own settings are correct.

## The limit on sending

Every message the Panel sends, a test or an invite, spends from **one budget
for the whole Panel**: ten a minute. The limit is not on any caller; it is on
the Panel's standing with the relay and with the recipients' providers, which a
hijacked administrator session could burn through as fast as a careless one.
It is on under `KRAKEN_RATE_LIMITS=all` and off under every narrower setting;
see [limits and logging](/wiki/configure/limits-and-logging/).

## In the audit log

| action | meaning |
| --- | --- |
| `settings:smtp` | the relay settings were saved (never what they were saved to) |
| `mail.test:<address>` | a test message was sent to that address; the row's status is 200 when the relay took it, 424 when it refused |
