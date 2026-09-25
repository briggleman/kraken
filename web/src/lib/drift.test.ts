// The container-drift badge (#340). The agent used to report only a count, so
// the badge could say "1 untracked" and nothing more; from 0.54.0 it names the
// containers it is counting, and these pin both readings — the named one and the
// count that an older agent still leaves as the only thing to go on.

import { beforeEach, describe, expect, it } from "vitest";

import { fleet } from "./fleet.svelte";
import { containerDrift, pendingRemovalsNote, stoppedContainers, stoppedLabel } from "./views.svelte";
import type { Node, Server } from "@/api/types";

const NODE_ID = "node-1";

function node(extra: Partial<Node> = {}): Node {
  return {
    id: NODE_ID,
    name: "abyss-lnx",
    status: "online",
    agent_version: "0.54.0",
    ...extra,
  } as Node;
}

function server(id: string, state: Server["state"]): Server {
  return {
    id,
    name: id + "-name",
    spec_id: "spec-1",
    node_id: NODE_ID,
    kind: "linux-native",
    state,
    vars: {},
    ports: { game: 27015 },
    memory_mb: 4096,
    created_at: "2026-09-17T09:00:00Z",
  };
}

beforeEach(() => {
  fleet.servers = [];
});

describe("containerDrift", () => {
  it("falls back to the count when the agent names nothing", () => {
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(node({ running_servers: 2 }));
    expect(drift).toMatchObject({ running: 2, delta: 1, word: "untracked" });
    // Nothing to name: the badge shows the count alone, as it did before.
    expect(drift?.items).toEqual([]);
  });

  it("names an untracked container", () => {
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(
      node({
        running_servers: 2,
        managed_containers: [
          { server_id: "a", container_name: "kraken_a" },
          { server_id: "ghost", container_name: "kraken_ghost" },
        ],
      }),
    );
    expect(drift?.word).toBe("untracked");
    expect(drift?.delta).toBe(1);
    expect(drift?.items).toEqual([{ server_id: "ghost", label: "kraken_ghost" }]);
  });

  it("names a missing container by the row that claims it", () => {
    fleet.servers = [server("a", "running"), server("b", "running")];
    const drift = containerDrift(
      node({
        running_servers: 1,
        managed_containers: [{ server_id: "a", container_name: "kraken_a" }],
      }),
    );
    expect(drift?.word).toBe("missing");
    expect(drift?.items).toEqual([{ server_id: "b", label: "b-name" }]);
  });

  // An install pass owns its row: it is not running, so it cannot be missing,
  // and its one-shot container carries the same server id, so it is not
  // untracked either. This is the transient that used to light the badge for the
  // length of every install.
  it("leaves an installing row alone", () => {
    fleet.servers = [server("a", "running"), server("b", "installing")];
    const drift = containerDrift(
      node({
        running_servers: 2,
        managed_containers: [
          { server_id: "a", container_name: "kraken_a" },
          { server_id: "b", container_name: "kraken_b_install" },
        ],
      }),
    );
    expect(drift).toBeUndefined();
  });

  // #381: after a reinstall the Agent's data-dir guard has removed the exited
  // game container and the install container is gone with the verdict, so the
  // node reports nothing for an offline row until START recreates it. That is
  // the expected shape, not an anomaly: neither untracked (there is no
  // container to be untracked) nor missing (the row never claimed running),
  // and nothing is owed a removal.
  it("leaves an offline row with no container alone", () => {
    fleet.servers = [server("a", "running"), server("b", "offline")];
    // A removal owed for some OTHER server, so the removals line is live and
    // the question is whether the offline row joins it.
    const named = node({
      running_servers: 1,
      managed_containers: [{ server_id: "a", container_name: "kraken_a" }],
      pending_removals: [
        { server_id: "gone", delete_data: false, requested_at: "2026-09-25T08:00:00Z", attempts: 1 },
      ],
    });
    expect(containerDrift(named)).toBeUndefined();
    const owed = pendingRemovalsNote(named);
    expect(owed?.count).toBe(1);
    expect(owed?.title).toContain("gone (");
    expect(owed?.title).not.toContain("b-name");
    expect(owed?.title).not.toContain("b (");
    // An agent that reports only the count reads the same: one running row,
    // one running container.
    expect(containerDrift(node({ running_servers: 1 }))).toBeUndefined();
    // And with nothing at all on the node — the reinstalled server alone.
    fleet.servers = [server("b", "offline")];
    expect(containerDrift(node({ running_servers: 0, managed_containers: [] }))).toBeUndefined();
  });

  it("says nothing about a node that is offline or never contacted", () => {
    fleet.servers = [server("a", "running")];
    expect(containerDrift(node({ status: "offline", running_servers: 9 }))).toBeUndefined();
    expect(containerDrift(node({ agent_version: "", running_servers: 9 }))).toBeUndefined();
  });
});

// #385: an agent that sets containers_reported lists stopped containers too,
// each with its state. Untracked and missing both filter on `running`; a stopped
// container is neither, and the band counts it beside the running figure.
describe("containerDrift with container states", () => {
  const reported = (extra: Partial<Node>) => node({ agent_version: "0.59.0", containers_reported: true, ...extra });

  it("counts a stopped container as neither untracked nor missing", () => {
    // a runs; b is offline and keeps its exited container; c's row is gone but
    // its container sits exited on the host — an orphan holding no memory or
    // ports, so not the badge's business.
    fleet.servers = [server("a", "running"), server("b", "offline")];
    const n = reported({
      running_servers: 1,
      managed_containers: [
        { server_id: "a", container_name: "kraken_a", state: "running" },
        { server_id: "b", container_name: "kraken_b", state: "exited" },
        { server_id: "c", container_name: "kraken_c", state: "exited" },
      ],
    });
    expect(containerDrift(n)).toBeUndefined();
    expect(stoppedContainers(n)).toBe(2);
  });

  it("calls a running row with only a stopped container missing", () => {
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(
      reported({
        running_servers: 0,
        managed_containers: [{ server_id: "a", container_name: "kraken_a", state: "exited" }],
      }),
    );
    expect(drift?.word).toBe("missing");
    expect(drift?.items).toEqual([{ server_id: "a", label: "a-name" }]);
  });

  it("names a running orphan and leaves a stopped one out of it", () => {
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(
      reported({
        running_servers: 2,
        managed_containers: [
          { server_id: "a", container_name: "kraken_a", state: "running" },
          { server_id: "ghost", container_name: "kraken_ghost", state: "running" },
          { server_id: "husk", container_name: "kraken_husk", state: "exited" },
        ],
      }),
    );
    expect(drift?.word).toBe("untracked");
    expect(drift?.items).toEqual([{ server_id: "ghost", label: "kraken_ghost" }]);
  });

  it("reads a reported empty list as no containers at all", () => {
    // The list is omitted from the JSON when empty; the marker is what says the
    // agent reported, so a running row with nothing behind it is missing.
    fleet.servers = [server("a", "running")];
    const drift = containerDrift(reported({ running_servers: 0 }));
    expect(drift?.word).toBe("missing");
    expect(drift?.items).toEqual([{ server_id: "a", label: "a-name" }]);
  });

  it("falls back to today's reading for an agent without the marker", () => {
    // 0.54–0.58: running containers only, no state, no marker. Every listed
    // container is taken as running, and a missing list falls back to the count.
    fleet.servers = [server("a", "running")];
    const old = node({
      running_servers: 2,
      managed_containers: [
        { server_id: "a", container_name: "kraken_a" },
        { server_id: "ghost", container_name: "kraken_ghost" },
      ],
    });
    expect(containerDrift(old)?.items).toEqual([{ server_id: "ghost", label: "kraken_ghost" }]);
    expect(containerDrift(node({ running_servers: 2 }))).toMatchObject({ delta: 1, word: "untracked", items: [] });
    // And no stopped count: this agent never says.
    expect(stoppedContainers(old)).toBe(0);
    expect(stoppedLabel(old)).toBeUndefined();
  });

  it("does not call a stopped owed container still running", () => {
    fleet.servers = [];
    const owed = pendingRemovalsNote(
      reported({
        managed_containers: [{ server_id: "gone", container_name: "kraken_gone", state: "exited" }],
        pending_removals: [
          { server_id: "gone", delete_data: false, requested_at: "2026-09-25T08:00:00Z", attempts: 1 },
        ],
      }),
    );
    expect(owed?.title).not.toContain("container still running");
  });
});

describe("stoppedLabel", () => {
  it("says how many containers are stopped, only when there are some", () => {
    const n = node({
      containers_reported: true,
      running_servers: 2,
      managed_containers: [
        { server_id: "a", container_name: "kraken_a", state: "running" },
        { server_id: "b", container_name: "kraken_b", state: "running" },
        { server_id: "c", container_name: "kraken_c", state: "exited" },
      ],
    });
    expect(stoppedLabel(n)).toBe("1 stopped");
    expect(
      stoppedLabel(node({ containers_reported: true, managed_containers: [{ server_id: "a", container_name: "kraken_a", state: "running" }] })),
    ).toBeUndefined();
    expect(stoppedLabel(node({ containers_reported: true }))).toBeUndefined();
  });

  it("counts every state that is not running", () => {
    const n = node({
      containers_reported: true,
      managed_containers: ["exited", "created", "dead", "paused"].map((state, i) => ({
        server_id: "s" + i,
        container_name: "kraken_s" + i,
        state,
      })),
    });
    expect(stoppedLabel(n)).toBe("4 stopped");
  });

  it("says nothing for a node that is offline or never contacted", () => {
    const managed_containers = [{ server_id: "a", container_name: "kraken_a", state: "exited" }];
    expect(stoppedLabel(node({ status: "offline", containers_reported: true, managed_containers }))).toBeUndefined();
    expect(stoppedLabel(node({ agent_version: "", containers_reported: true, managed_containers }))).toBeUndefined();
  });
});
