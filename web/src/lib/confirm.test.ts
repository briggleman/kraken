// The typed confirmation asks for the word on its own button. It used to be
// hard-wired to "delete", which a retire (#360) cannot ask for without the
// dialog contradicting itself; the mock's own shortcut — a global "retire" —
// would have demanded "retire" to delete a spec, a node or a file. The word is
// the opener's verb, so every caller that says "delete" is unchanged.

import { beforeEach, describe, expect, it } from "vitest";
import { confirmWord, openConfirm, ui } from "./state.svelte";

beforeEach(() => {
  ui.confirm = null;
});

describe("confirmWord", () => {
  it("is the opener's verb", () => {
    openConfirm("dragonwilds-02", null, { verb: "retire", noun: "server" });
    expect(confirmWord(ui.confirm)).toBe("retire");
  });

  it("stays delete for every opener that does not name a verb", () => {
    openConfirm("valheim", null, { noun: "spec" });
    expect(confirmWord(ui.confirm)).toBe("delete");
    openConfirm("saves", null, { noun: "folder", verb: "delete" });
    expect(confirmWord(ui.confirm)).toBe("delete");
  });

  it("is lower-case, so the comparison against what was typed is too", () => {
    expect(confirmWord({ verb: "Retire" })).toBe("retire");
  });

  it("falls back to delete with no dialog open", () => {
    expect(confirmWord(null)).toBe("delete");
    expect(confirmWord(undefined)).toBe("delete");
  });
});
