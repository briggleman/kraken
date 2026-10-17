// The invite sheet's state: an emailed link opens the Panel at /invite/<token>,
// and this module notices, takes the token out of the address bar, asks the
// Panel what the link is for, and holds the answer for the sheet.

import { api } from "@/api/client";
import type { InviteInfo } from "@/api/types";
import { inviteTokenFromPath } from "./invite";

export const invite = $state({
  // idle: this page was not opened from an invite link. loading / ready /
  // invalid: the sheet is up. done: the person is signed in, or gave up.
  status: "idle" as "idle" | "loading" | "ready" | "invalid" | "done",
  token: null as string | null,
  info: null as InviteInfo | null,
});

/** The sheet is up and the login sheet should stay out of its way. */
export const inviteActive = () => invite.status === "loading" || invite.status === "ready" || invite.status === "invalid";

/** Called once at boot, before the session is resolved: there is no session
 *  to resolve for a person who has not chosen a password yet. */
export function bootInvite(): void {
  const token = inviteTokenFromPath(location.pathname);
  if (!token) return;
  invite.token = token;
  invite.status = "loading";
  // The token leaves the address bar at once, so it does not sit in the
  // browser history or a screenshot longer than it has to.
  history.replaceState(null, "", "/");
  void api
    .getInvite(token)
    .then((info) => {
      invite.info = info;
      invite.status = "ready";
    })
    .catch(() => {
      invite.status = "invalid";
    });
}

export function inviteDone(): void {
  invite.status = "done";
  invite.token = null;
  invite.info = null;
}
