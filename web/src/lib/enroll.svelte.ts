// Node enrollment — shared by the add-node sheet and wizard step 3. One
// lifecycle: mint a bootstrap token, poll its enroll status, register the
// node when the agent redeems it, then watch the fleet until the node
// reports online. Semantics carried over from the old ConnectNode component:
// tunnel mode registers with the enrollment's tunnel_id and the install
// tab's OS (a tunnel agent can't be probed — #112); direct mode registers
// the agent's self-reported address.

import { api } from "@/api/client";
import type { EnrollStatus } from "@/api/types";
import { fleet, refreshFleet } from "./fleet.svelte";

export interface EnrollLine {
  text: string;
  pending: boolean;
}

export type EnrollMode = "tunnel" | "direct";

export class Enrollment {
  token = $state("");
  caFingerprint = $state("");
  expiresAt = $state("");
  status = $state<"idle" | "waiting" | "redeemed" | "registered" | "online" | "expired" | "error">(
    "idle",
  );
  nodeName = $state("");
  lines = $state<EnrollLine[]>([]);
  error = $state<string | null>(null);

  #poll: ReturnType<typeof setInterval> | undefined;
  #nodePoll: ReturnType<typeof setInterval> | undefined;
  #registeredId: string | null = null;

  get badge(): string {
    switch (this.status) {
      case "idle":
        return "waiting for the agent";
      case "waiting":
        return "waiting for the agent to enroll";
      case "redeemed":
      case "registered":
        return "enrolled";
      case "online":
        return "online";
      case "expired":
        return "token expired — generate another";
      case "error":
        return "enrollment failed";
    }
  }

  async generate(mode: EnrollMode, os: "linux" | "windows") {
    this.stop();
    this.error = null;
    try {
      const t = await api.createBootstrapToken();
      this.token = t.token;
      this.caFingerprint = t.ca_fingerprint ?? "";
      this.expiresAt = t.expires_at;
      this.status = "waiting";
      this.lines = [
        { text: "one-time enrollment token generated — valid 15 minutes", pending: false },
        {
          text: "waiting for the agent to enroll — run the command on the remote host",
          pending: true,
        },
      ];
      this.#poll = setInterval(() => void this.#check(mode, os), 3000);
    } catch (e) {
      this.status = "error";
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async #check(mode: EnrollMode, os: "linux" | "windows") {
    let st: EnrollStatus;
    try {
      st = await api.enrollStatus(this.token);
    } catch {
      return; // transient; keep polling
    }
    if (st.status === "expired") {
      this.status = "expired";
      this.lines.push({ text: "the token expired unredeemed", pending: false });
      this.stop();
      return;
    }
    if (st.status !== "redeemed" || this.status !== "waiting") return;
    this.status = "redeemed";
    this.nodeName = st.node_name ?? "";
    const hosts = st.hosts ?? [];
    this.lines[1] = {
      text:
        "agent enrolled" +
        (st.ip ? " from " + st.ip : "") +
        (hosts.length ? " — advertised hosts: " + hosts.join(", ") : ""),
      pending: false,
    };
    clearInterval(this.#poll);
    this.#poll = undefined;
    // register: name/os/wine come from the agent unless tunnel mode, which
    // can't be probed until first contact
    try {
      const n = await api.registerNode({
        address:
          mode === "direct" && hosts.length
            ? hosts[0] + ":" + (st.agent_port ?? 9090)
            : undefined,
        connection_mode: mode,
        tunnel_id: mode === "tunnel" ? st.tunnel_id : undefined,
        os: mode === "tunnel" ? os : undefined,
        name: mode === "tunnel" ? st.node_name || undefined : undefined,
      });
      this.#registeredId = n.id;
      this.status = "registered";
      this.lines.push({ text: "node registered — waiting for it to report online", pending: true });
      await api.nodeInfo(n.id).catch(() => {}); // first contact may lag
      await refreshFleet();
      this.#nodePoll = setInterval(() => void this.#checkOnline(), 4000);
      void this.#checkOnline();
    } catch (e) {
      this.status = "error";
      this.error = e instanceof Error ? e.message : String(e);
      this.lines.push({
        text: "registration failed — " + (this.error ?? "unknown"),
        pending: false,
      });
    }
  }

  async #checkOnline() {
    if (!this.#registeredId) return;
    await refreshFleet();
    const n = fleet.nodes.find((x) => x.id === this.#registeredId);
    if (n && (n.status === "online" || n.status === "partial")) {
      this.status = "online";
      const last = this.lines.length - 1;
      this.lines[last] = {
        text: `node "${n.name}" (${n.os}) reported online`,
        pending: false,
      };
      this.nodeName = n.name;
      this.stop();
    }
  }

  stop() {
    if (this.#poll) clearInterval(this.#poll);
    if (this.#nodePoll) clearInterval(this.#nodePoll);
    this.#poll = undefined;
    this.#nodePoll = undefined;
  }

  reset() {
    this.stop();
    this.token = "";
    this.caFingerprint = "";
    this.status = "idle";
    this.nodeName = "";
    this.lines = [];
    this.error = null;
    this.#registeredId = null;
  }
}
