// Required game settings (spec `required: true`). The panel refuses to start or
// restart a server while one is empty, so the UI has two questions to answer
// ahead of that refusal: on the deploy form, "can this game start the moment its
// install lands?", and on the Settings tab, "which of these is blocking it?".
// Both are asked of the same rule the panel applies — empty or only whitespace
// is missing — so the UI can never promise a start the panel will turn down.

import type { SettingField, Spec } from "@/api/types";

/** Whether `value` fails a required field. Whitespace is not a value: an owner
 *  id of three spaces is still blank to the game. */
export function isMissing(field: SettingField, value: string | undefined): boolean {
  return !!field.required && !(value ?? "").trim();
}

/** Whether a required field's shown value is the spec's default rather than
 *  one of the server's own: the panel lists it in `from_spec` when the server
 *  stores a blank for it and the spec has a default, which the blank yields to
 *  (#367). Only asked of required fields — a blank optional one is a value the
 *  operator chose — and only while the operator has not typed into it: an edit
 *  is theirs the moment it exists, even an empty one. */
export function isFromSpec(
  field: SettingField,
  fromSpec: readonly string[] | undefined,
  edited: string | undefined,
): boolean {
  return !!field.required && edited === undefined && (fromSpec ?? []).includes(field.key);
}

/** The required fields a freshly deployed server of `spec` starts without. A
 *  new server boots on the spec's own defaults, so these are the required
 *  fields with no default — the ones an operator must fill before a first
 *  start can succeed. Declared order, so a message lists them as the Settings
 *  tab does. */
export function missingOnDeploy(spec: Spec | undefined): SettingField[] {
  const out: SettingField[] = [];
  for (const g of spec?.settings?.groups ?? []) {
    for (const f of g.fields) {
      if (isMissing(f, f.default)) out.push(f);
    }
  }
  return out;
}

/** "A", "A and B", "A, B and C" — the fields by the names the Settings tab
 *  shows them under. */
export function fieldList(fields: SettingField[]): string {
  const names = fields.map((f) => f.label || f.key);
  if (names.length <= 1) return names[0] ?? "";
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}
