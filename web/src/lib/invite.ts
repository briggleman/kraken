// Account invites, the pure parts: the path an emailed link opens, the chip
// the users list shows for an account that has not chosen a password yet, and
// whether the create-account form can offer an invite at all.

import type { InviteState, PanelSettings } from "@/api/types";

// The link in the email is <public url>/invite/<token>; the token is 32 random
// bytes, base64url, so 43 characters of [A-Za-z0-9_-]. The floor of 16 keeps
// a stray /invite/ path from ever being read as a token.
const INVITE_PATH_RE = /^\/invite\/([A-Za-z0-9_-]{16,})\/?$/;

/** The one-time token in an invite path, or null when the path is not one. */
export function inviteTokenFromPath(pathname: string): string | null {
  const m = INVITE_PATH_RE.exec(pathname);
  return m ? m[1] : null;
}

/** How long a live invite has left, as the chip and the accept sheet say it. */
export function inviteLeft(expiresAt: string, now = Date.now()): string {
  const ms = new Date(expiresAt).getTime() - now;
  if (!Number.isFinite(ms) || ms <= 0) return "expired";
  const mins = Math.floor(ms / 60_000);
  if (mins < 1) return "under a minute left";
  if (mins < 90) return `${mins}m left`;
  const hours = Math.round(ms / 3_600_000);
  return `${hours}h left`;
}

export type InviteChip = { text: string; cls: "ok" | "bad" };

/** The chip for an account's open invite, or null when it has none. A live
 *  invite reads in the ok colour; an expired or failed one prevents a sign-in,
 *  so it takes the caution reading. */
export function inviteChip(inv: InviteState | undefined | null, now = Date.now()): InviteChip | null {
  if (!inv) return null;
  switch (inv.state) {
    case "sent": {
      const left = inviteLeft(inv.expires_at, now);
      return left === "expired" ? { text: "invite expired", cls: "bad" } : { text: `invited · ${left}`, cls: "ok" };
    }
    case "expired":
      return { text: "invite expired", cls: "bad" };
    case "failed":
      return { text: "invite failed", cls: "bad" };
  }
}

/** Whether the create form may offer an invite: a relay, a public address and
 *  an address to send to. Settings that failed to load offer nothing. */
export function inviteModeAvailable(
  settings: Pick<PanelSettings, "smtp_configured" | "public_url"> | null | undefined,
  email: string,
): boolean {
  return !!settings?.smtp_configured && !!settings.public_url && email.trim().includes("@");
}
