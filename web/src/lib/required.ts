// Required game settings (spec `required: true`). The panel refuses to start or
// restart a server while one is empty, so the UI has two questions to answer
// ahead of that refusal: on the deploy form, "can this game start the moment its
// install lands?", and on the Settings tab, "which of these is blocking it?".
// Both are asked of the same rule the panel applies — empty or only whitespace
// is missing — so the UI can never promise a start the panel will turn down.

import type { SettingField, Spec } from "@/api/types";

const blank = (v: string | undefined) => !(v ?? "").trim();

/** Whether a required field's blank `value` falls back to the spec's default.
 *  The panel's own rule (spec.ResolveSettings, #367): a required field stored
 *  empty or whitespace-only takes the spec's default when that default is not
 *  blank itself. So clearing such a field hands it back to the default. */
function yieldsToDefault(field: SettingField, value: string | undefined): boolean {
  return !!field.required && blank(value) && !blank(field.default);
}

/** Whether `value` fails a required field. Whitespace is not a value: an owner
 *  id of three spaces is still blank to the game. A blank that falls back to a
 *  spec default is not missing, because the panel starts the server on that
 *  default. */
export function isMissing(field: SettingField, value: string | undefined): boolean {
  return !!field.required && blank(value) && !yieldsToDefault(field, value);
}

/** Whether a required field's value is (or will be, once saved) the spec's
 *  default rather than one of the server's own. Untouched, it is what the
 *  panel says: listed in `from_spec` (#367). Once the operator types, it
 *  mirrors the panel's rule for what the save will store: a value of their own
 *  is theirs, while clearing it hands it back to a non-blank spec default. Only
 *  asked of required fields — a blank optional one is a value the operator
 *  chose. */
export function isFromSpec(
  field: SettingField,
  fromSpec: readonly string[] | undefined,
  edited: string | undefined,
): boolean {
  if (!field.required) return false;
  if (edited === undefined) return (fromSpec ?? []).includes(field.key);
  return yieldsToDefault(field, edited);
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
