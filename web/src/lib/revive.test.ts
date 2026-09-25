// The revive sheet's decisions (#360). Pure helpers: which nodes it offers and
// in what order, whether the old ports are free there, what the restore select
// reads, what it refuses before the click, and that it posts only what the
// operator changed — the Panel's defaults are the old node, memory and ports.

import { describe, expect, it } from "vitest";
import {
  backupOptions,
  effectiveRestore,
  reviveMemorySource,
  reviveSeedMemory,
  nodeFits,
  nodeOptionLabel,
  portsFree,
  retiredPortList,
  reviveBlock,
  reviveBody,
  reviveCandidates,
} from "./revive";
import { fmtWhen } from "./fmt";
import type { Backup, Node, Server, Spec } from "@/api/types";

function node(id: string, extra: Partial<Node> = {}): Node {
  return {
    id,
    name: id,
    os: "linux",
    wine_enabled: false,
    status: "online",
    address: "",
    public_host: "",
    total_memory_mb: 65536,
    allocated_memory_mb: 0,
    ports: { ranges: [{ start: 28000, end: 28100 }], allocated: [] },
    ...extra,
  };
}

function server(extra: Partial<Server> = {}): Server {
  return {
    id: "sv-1",
    name: "dragonwilds-02",
    spec_id: "spec-1",
    node_id: "",
    kind: "linux-native",
    state: "retired",
    vars: {},
    ports: {},
    memory_mb: 8192,
    retired_from_node_id: "behemoth",
    retired_ports: { game: 28010, query: 28011 },
    created_at: "",
    ...extra,
  };
}

const SPEC = { id: "spec-1", name: "Dragonwilds", slug: "dragonwilds", platforms: [{ kind: "linux-native", image: "" }], resources: { min_memory_mb: 4096 } } as unknown as Spec;

describe("reviveCandidates", () => {
  it("offers its old node first, then the online nodes that can run its platform", () => {
    const nodes = [
      node("titan"),
      node("win", { os: "windows" }),
      node("behemoth"),
      node("locked", { status: "cordoned" }),
      node("away", { status: "offline" }),
    ];
    expect(reviveCandidates(server(), nodes).map((n) => n.id)).toEqual(["behemoth", "titan"]);
  });

  it("keeps an old node that is away, so the sheet can say why it will not do", () => {
    const nodes = [node("titan"), node("behemoth", { status: "offline" })];
    const cands = reviveCandidates(server(), nodes);
    expect(cands.map((n) => n.id)).toEqual(["behemoth", "titan"]);
    expect(nodeOptionLabel(cands[0])).toBe("behemoth — offline");
    expect(nodeOptionLabel(node("x", { status: "cordoned" }))).toBe("x — locked");
    expect(reviveBlock(server(), SPEC, nodes, cands[0])).toMatch(/^behemoth is offline and takes no new servers/);
  });

  it("fits a platform the way the scheduler does", () => {
    expect(nodeFits(node("a", { wine_enabled: true }), "linux-wine")).toBe(true);
    expect(nodeFits(node("a"), "linux-wine")).toBe(false);
    expect(nodeFits(node("a", { os: "windows" }), "windows-native")).toBe(true);
    expect(nodeFits(node("a", { os: "windows" }), "linux-native")).toBe(false);
  });
});

describe("portsFree", () => {
  const ports = retiredPortList(server());

  it("is true only when every old port is in the pool and unallocated", () => {
    expect(ports).toEqual([28010, 28011]);
    expect(portsFree(ports, node("a"))).toBe(true);
    expect(portsFree(ports, node("a", { ports: { ranges: [{ start: 28000, end: 28100 }], allocated: [28011] } }))).toBe(false);
    expect(portsFree(ports, node("a", { ports: { ranges: [{ start: 27000, end: 27100 }], allocated: [] } }))).toBe(false);
  });

  it("makes no claim with nothing to reuse or no node", () => {
    expect(portsFree([], node("a"))).toBe(false);
    expect(portsFree(ports, undefined)).toBe(false);
  });
});

describe("backupOptions", () => {
  it("offers ready archives newest first, the first marked latest", () => {
    const b = (id: string, ms: number, state: Backup["state"] = "ready", size = 2.4 * 1024 ** 3): Backup => ({
      id,
      name: id,
      size,
      created_ms: ms,
      state,
      replication: "",
    });
    const opts = backupOptions([b("nightly", 1000), b("final-before-retire", 3000), b("broken", 4000, "failed"), b("pending", 5000, "pending")]);
    expect(opts.map((o) => o.id)).toEqual(["final-before-retire", "nightly"]);
    expect(opts[0].label).toBe(`${fmtWhen(3000)} · final-before-retire · 2.4G — latest`);
    expect(opts[1].label).toBe(`${fmtWhen(1000)} · nightly · 2.4G`);
  });
});

describe("reviveBlock", () => {
  const nodes = [node("behemoth")];

  it("refuses while a removal is still owed for it", () => {
    const owed = [node("behemoth", { pending_removals: [{ server_id: "sv-1", delete_data: true, requested_at: "", attempts: 1 }] })];
    expect(reviveBlock(server(), SPEC, owed, owed[0])).toMatch(/still being removed from behemoth/);
  });

  it("refuses a spec that is gone or no longer offers its platform", () => {
    expect(reviveBlock(server(), undefined, nodes, nodes[0])).toMatch(/spec it was built from no longer exists/);
    expect(reviveBlock(server({ kind: "linux-wine" }), SPEC, nodes, nodes[0])).toMatch(/no longer offers the linux-wine platform/);
  });

  it("refuses with no node to place it on, and allows the rest", () => {
    expect(reviveBlock(server(), SPEC, nodes, undefined)).toMatch(/no node can run/);
    expect(reviveBlock(server(), SPEC, nodes, nodes[0])).toBe("");
  });
});

describe("the restore choice", () => {
  const opts = [
    { id: "latest", label: "", bytes: 1 },
    { id: "older", label: "", bytes: 1 },
  ];

  it("defaults to the latest on the old node, and is none anywhere else", () => {
    expect(effectiveRestore(true, false, "", opts)).toBe("latest");
    expect(effectiveRestore(false, false, "", opts)).toBe("");
    expect(effectiveRestore(false, true, "older", opts)).toBe("");
  });

  it("keeps an explicit none chosen before the list arrived", () => {
    // the operator picked none while the archives were still being read…
    expect(effectiveRestore(true, true, "", [])).toBe("");
    // …and the list landing does not put the latest back
    expect(effectiveRestore(true, true, "", opts)).toBe("");
  });

  it("comes back to the latest after a trip to another node, when untouched", () => {
    expect(effectiveRestore(false, false, "", opts)).toBe("");
    expect(effectiveRestore(true, false, "", opts)).toBe("latest");
    // and to the operator's pick when they made one
    expect(effectiveRestore(true, true, "older", opts)).toBe("older");
  });
});

describe("reviveSeedMemory", () => {
  it("is its old figure, or the spec's allocation when the minimum was raised past it", () => {
    expect(reviveSeedMemory(server({ memory_mb: 8192 }), SPEC)).toBe(8192);
    const raised = { ...SPEC, resources: { min_memory_mb: 12288, recommended_memory_mb: 16384 } } as Spec;
    expect(reviveSeedMemory(server({ memory_mb: 8192 }), raised)).toBe(16384);
    const minOnly = { ...SPEC, resources: { min_memory_mb: 12288 } } as Spec;
    expect(reviveSeedMemory(server({ memory_mb: 8192 }), minOnly)).toBe(12288);
  });

  it("does not post the seed back as a change", () => {
    const raised = { ...SPEC, resources: { min_memory_mb: 12288 } } as Spec;
    const seed = reviveSeedMemory(server(), raised);
    expect(reviveBody(server(), { nodeId: "behemoth", memoryMb: seed, restoreId: "", start: false, steamGuard: "" }, seed)).toEqual({});
  });
});

describe("reviveMemorySource", () => {
  it("says what it had before when the seed is its old figure", () => {
    const sv = server({ memory_mb: 8192 });
    expect(reviveMemorySource(sv, SPEC, reviveSeedMemory(sv, SPEC))).toBe("what it had before");
  });

  it("says the minimum was raised only when there was an old figure and the minimum is now above it", () => {
    const raised = { ...SPEC, resources: { min_memory_mb: 12288, recommended_memory_mb: 16384 } } as Spec;
    const sv = server({ memory_mb: 8192 });
    expect(reviveMemorySource(sv, raised, reviveSeedMemory(sv, raised))).toBe(
      "the spec's allocation — its minimum was raised since it was retired",
    );
  });

  it("says only the spec's allocation when no figure was stored", () => {
    const withRec = { ...SPEC, resources: { min_memory_mb: 4096, recommended_memory_mb: 8192 } } as Spec;
    const sv = server({ memory_mb: 0 });
    expect(reviveSeedMemory(sv, withRec)).toBe(8192);
    expect(reviveMemorySource(sv, withRec, reviveSeedMemory(sv, withRec))).toBe("the spec's allocation");
    // nothing stored and nothing in the spec either: still not "what it had before"
    const bare = { ...SPEC, resources: { min_memory_mb: 0 } } as Spec;
    expect(reviveMemorySource(sv, bare, reviveSeedMemory(sv, bare))).toBe("the spec's allocation");
    expect(reviveMemorySource(sv, undefined, 0)).toBe("the spec's allocation");
  });
});

describe("a chosen node that goes away mid-sheet", () => {
  it("stays in the picker, named with its status, and the block says so", () => {
    const nodes = [node("behemoth"), node("titan", { status: "offline" })];
    const cands = reviveCandidates(server(), nodes, "titan");
    expect(cands.map((n) => n.id)).toEqual(["behemoth", "titan"]);
    expect(reviveBlock(server(), SPEC, nodes, nodes[1])).toBe("titan is offline and takes no new servers — pick another node");
  });
});

describe("reviveBody", () => {
  const base = { nodeId: "behemoth", memoryMb: 8192, restoreId: "", start: false, steamGuard: "" };

  it("posts nothing when everything is as it was", () => {
    expect(reviveBody(server(), base)).toEqual({});
  });

  it("posts only what changed", () => {
    expect(reviveBody(server(), { ...base, nodeId: "titan", memoryMb: 12288, restoreId: "b1", start: true, steamGuard: " 7Q2X " })).toEqual({
      node_id: "titan",
      memory_mb: 12288,
      restore_backup_id: "b1",
      start: true,
      steam_guard_code: "7Q2X",
    });
  });
});
