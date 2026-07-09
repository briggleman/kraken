import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import yaml from "js-yaml";
import Editor from "react-simple-code-editor";
import { api } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import { highlightConfig } from "@/components/highlight";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { IconButton } from "@ds/components/core/IconButton";
import { Toggle } from "@ds/components/core/Toggle";
import { Select as DSelect } from "@ds/components/core/Select";

const mono = "var(--font-mono)";

type Mode = "yaml" | "json";
type View = "form" | "code";

const YAML_TEMPLATE = `name: New Game
slug: new-game
steam_app_ids:
  linux: 0
platforms:
  - kind: linux-native
    image: registry/kraken/steam-base:latest
install:
  script: steamcmd +login anonymous +app_update {{APP_ID}} validate +quit
startup:
  command: ./server -port {{PORT_GAME}}
  stop: { type: signal, value: SIGINT }
variables:
  - { key: SERVER_NAME, label: Server name, type: string, default: My Server, user_editable: true }
ports:
  - { name: game, protocol: udp, default: 27015, required: true }
settings:
  groups:
    - id: world
      label: World
      fields:
        - { key: world_name, label: World name, type: string, default: Midgard }
        - { key: pvp, label: Enable PvP, type: bool, default: "false" }
    - id: network
      label: Network
      fields:
        - { key: max_players, label: Max players, type: int, min: 1, max: 64, default: "16" }
config_files:
  - path: /data/server.cfg
    format: source-cvar
    bindings:
      servername: world_name
      maxplayers: max_players
      sv_pvp: { from: pvp, map: { "true": "1", "false": "0" } }
resources:
  min_memory_mb: 2048
`;

// emit converts an object to text in the requested mode.
function emit(obj: unknown, mode: Mode): string {
  return mode === "yaml" ? yaml.dump(obj, { lineWidth: 100 }) : JSON.stringify(obj, null, 2);
}

const PLATFORM_KINDS = ["linux-native", "linux-wine", "windows-native"];
const FIELD_TYPES = ["string", "text", "int", "float", "bool", "enum", "password"];

export function SpecEditor() {
  const { id } = useParams();
  const isNew = !id || id === "new";
  const navigate = useNavigate();
  const { confirm } = useDialog();
  const [mode, setMode] = useState<Mode>("yaml");
  const [view, setView] = useState<View>("form");
  const [text, setText] = useState(isNew ? YAML_TEMPLATE : "");
  // form holds the parsed spec object the form edits; it stays in sync with text.
  const [form, setForm] = useState<any>(isNew ? yaml.load(YAML_TEMPLATE) : null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(isNew);

  useEffect(() => {
    if (isNew) return;
    api
      .getSpec(id!)
      .then((sp) => {
        setText(emit(sp, "yaml"));
        setForm(sp);
      })
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load"))
      .finally(() => setLoaded(true));
  }, [id, isNew]);

  // applyForm updates the form object and re-serializes the code text from it.
  const applyForm = (next: any) => {
    setForm(next);
    setText(emit(next, mode));
  };
  // upd clones the form, lets the caller mutate the draft, then applies it.
  const upd = (mut: (draft: any) => void) => {
    const next = structuredClone(form ?? {});
    mut(next);
    applyForm(next);
  };

  // Toggle code format: parse current text and re-emit.
  const switchMode = (next: Mode) => {
    if (next === mode) return;
    try {
      const obj = yaml.load(text);
      setText(emit(obj, next));
      setMode(next);
      setError(null);
    } catch (e) {
      setError("Can't switch format — fix the document first: " + (e instanceof Error ? e.message : ""));
    }
  };

  // Switch between Form and Code. Going to Form re-parses the text so manual code
  // edits are reflected; going to Code uses the already-synced text.
  const switchView = (next: View) => {
    if (next === view) return;
    if (next === "form") {
      try {
        setForm(yaml.load(text));
        setError(null);
      } catch (e) {
        setError("Can't open the form — fix the document first: " + (e instanceof Error ? e.message : ""));
        return;
      }
    }
    setView(next);
  };

  const save = async () => {
    try {
      yaml.load(text);
    } catch (e) {
      setError("Invalid " + mode.toUpperCase() + ": " + (e instanceof Error ? e.message : ""));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (isNew) {
        const created = await api.createSpecRaw(text);
        navigate(`/specs/${created.id}`);
      } else {
        await api.updateSpecRaw(id!, text);
        navigate("/specs");
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "save failed");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (isNew) return;
    if (!(await confirm({ title: "Delete spec", message: "Delete this spec? Servers using it will be blocked.", confirmLabel: "Delete", danger: true }))) return;
    try {
      await api.deleteSpec(id!);
      navigate("/specs");
    } catch (e) {
      setError(e instanceof Error ? e.message : "delete failed");
    }
  };

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div onClick={() => navigate("/specs")} style={{ display: "inline-flex", alignItems: "center", gap: 8, cursor: "pointer", marginBottom: 20, fontFamily: mono, fontSize: 12.5, color: "var(--text-muted)" }}>
        ← <span style={{ color: "var(--text-secondary)" }}>game specs</span> <span style={{ opacity: 0.4 }}>/</span>{" "}
        <span style={{ color: "var(--accent)" }}>{isNew ? "new" : id?.slice(0, 8)}</span>
      </div>

      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", marginBottom: 22, gap: 16, flexWrap: "wrap" }}>
        <div>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// {isNew ? "NEW SPEC" : "EDIT SPEC"}</div>
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
            {isNew ? "New spec" : "Edit spec"}
          </h1>
        </div>
        <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
          <Segmented options={[["form", "Form"], ["code", "Code"]]} value={view} onChange={(v) => switchView(v as View)} />
          {view === "code" && (
            <Segmented options={[["yaml", "YAML"], ["json", "JSON"]]} value={mode} onChange={(v) => switchMode(v as Mode)} />
          )}
          {!isNew && <Button variant="danger" icon="x" onClick={remove}>Delete</Button>}
          <Button variant="ghost" onClick={() => navigate("/specs")}>Cancel</Button>
          <Button variant="primary" icon="check" disabled={busy || !loaded} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
        </div>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 12, whiteSpace: "pre-wrap" }}>{error}</div>}

      {view === "code" ? (
        <>
          <div style={{ fontFamily: mono, fontSize: 12, color: "var(--text-muted)", marginBottom: 10 }}>
            Placeholders like <code style={{ color: "var(--accent)" }}>{"{{APP_ID}}"}</code> /{" "}
            <code style={{ color: "var(--accent)" }}>{"{{PORT_GAME}}"}</code> resolve at deploy time.
          </div>
          <Card padding={0} style={{ overflow: "auto", background: "var(--bg-inset)", minHeight: 520 }}>
            <Editor
              value={text}
              onValueChange={setText}
              highlight={highlightConfig}
              padding={18}
              textareaId="spec-editor"
              style={{
                fontFamily: mono,
                fontSize: 13,
                lineHeight: 1.6,
                minHeight: 520,
                color: "var(--text-primary)",
              }}
            />
          </Card>
        </>
      ) : form ? (
        <SpecForm form={form} upd={upd} />
      ) : (
        <div style={{ fontFamily: mono, color: "var(--text-muted)" }}>Loading…</div>
      )}
    </main>
  );
}

/* ---------- form ---------- */

function SpecForm({ form, upd }: { form: any; upd: (mut: (d: any) => void) => void }) {
  const platforms: any[] = form.platforms ?? [];
  const variables: any[] = form.variables ?? [];
  const ports: any[] = form.ports ?? [];
  const groups: any[] = form.settings?.groups ?? [];

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <Section title="Identity">
        <Row>
          <Field grow><Input label="NAME" value={form.name ?? ""} onChange={(e) => upd((d) => (d.name = e.target.value))} placeholder="Counter-Strike 2" /></Field>
          <Field><Input label="SLUG" mono value={form.slug ?? ""} onChange={(e) => upd((d) => (d.slug = e.target.value))} placeholder="cs2" /></Field>
        </Row>
        <Field grow><Input label="DESCRIPTION" value={form.description ?? ""} onChange={(e) => upd((d) => (d.description = e.target.value))} placeholder="Short summary" /></Field>
        <Row>
          <Field grow><Input label="BANNER URL" mono value={form.banner_url ?? ""} onChange={(e) => upd((d) => (d.banner_url = e.target.value || undefined))} placeholder="https://…/header.jpg" /></Field>
          <Field grow><Input label="ICON URL" mono value={form.icon_url ?? ""} onChange={(e) => upd((d) => (d.icon_url = e.target.value || undefined))} placeholder="https://…/icon.jpg" /></Field>
        </Row>
        <Row>
          <Field><Num label="STEAM APP ID (LINUX)" value={form.steam_app_ids?.linux} onChange={(v) => upd((d) => { d.steam_app_ids = { ...d.steam_app_ids, linux: v }; })} /></Field>
          <Field><Num label="STEAM APP ID (WINDOWS)" value={form.steam_app_ids?.windows} onChange={(v) => upd((d) => { d.steam_app_ids = { ...d.steam_app_ids, windows: v }; })} /></Field>
        </Row>
      </Section>

      <Section title="Platforms" onAdd={() => upd((d) => { (d.platforms ??= []).push({ kind: "linux-native", image: "" }); })}>
        {platforms.length === 0 && <Empty>No platforms — add at least one.</Empty>}
        {platforms.map((p, i) => (
          <div key={i}>
            <Row onRemove={() => upd((d) => d.platforms.splice(i, 1))}>
              <Field><Select label="KIND" value={p.kind} options={PLATFORM_KINDS} onChange={(v) => upd((d) => (d.platforms[i].kind = v))} /></Field>
              <Field grow><Input label="IMAGE" mono value={p.image ?? ""} onChange={(e) => upd((d) => (d.platforms[i].image = e.target.value))} placeholder="registry/image:tag" /></Field>
            </Row>
            {/* Per-platform overrides: a linux-wine placement of a Windows-first
                game needs a different install (Linux SteamCMD + forced platform
                type) and launch (xvfb-run wine …) than the spec-level commands.
                Shown for linux-wine, or whenever an override is already set. */}
            {(p.kind === "linux-wine" || p.install_script || p.startup_command) && (
              <div style={{ display: "grid", gap: 10, margin: "8px 0 4px 12px" }}>
                <Field grow><Area label="INSTALL SCRIPT OVERRIDE (OPTIONAL — replaces the spec install for this platform)" rows={2} value={p.install_script ?? ""} onChange={(v) => upd((d) => (d.platforms[i].install_script = v || undefined))} placeholder="steamcmd +@sSteamCmdForcePlatformType windows +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit" /></Field>
                <Field grow><Area label="STARTUP COMMAND OVERRIDE (OPTIONAL — replaces the spec startup for this platform)" rows={2} value={p.startup_command ?? ""} onChange={(v) => upd((d) => (d.platforms[i].startup_command = v || undefined))} placeholder="wine-headless /data/<Game>/Binaries/Win64/<Server>-Win64-Shipping.exe -log -PORT={{PORT_GAME}}" /></Field>
              </div>
            )}
          </div>
        ))}
      </Section>

      <Section title="Install">
        <Field grow><Area label="SCRIPT" rows={2} value={form.install?.script ?? ""} onChange={(v) => upd((d) => { d.install = { ...d.install, script: v }; })} placeholder="steamcmd +login anonymous +app_update {{APP_ID}} validate +quit" /></Field>
        <ToggleRow label="BepInEx compatible (Unity games with mod support)" checked={!!form.install?.bepinex_compatible} onChange={(v) => upd((d) => { d.install = { ...d.install, bepinex_compatible: v }; })} />
        {form.install?.bepinex_compatible && (
          <>
            <Field grow><Area label="BEPINEX INSTALL SCRIPT (runs after the install above)" rows={2} value={form.install?.bepinex_script ?? ""} onChange={(v) => upd((d) => { d.install = { ...d.install, bepinex_script: v }; })} placeholder="cd /data && curl -sSL -o /tmp/b.zip <pack-url> && unzip -oq /tmp/b.zip -d /data" /></Field>
            <Field grow><Area label="BEPINEX STARTUP COMMAND (launch via the Doorstop loader)" rows={2} value={form.startup?.bepinex_command ?? ""} onChange={(v) => upd((d) => { d.startup = { ...d.startup, bepinex_command: v }; })} placeholder="cd /data && export DOORSTOP_ENABLE=TRUE && ... ./run_bepinex.sh ..." /></Field>
          </>
        )}
        <ToggleRow label="Requires Steam login (real account + 2FA)" checked={!!form.install?.requires_steam_login} onChange={(v) => upd((d) => { d.install = { ...d.install, requires_steam_login: v }; })} />
      </Section>

      <Section title="Startup">
        <Field grow><Area label="COMMAND" rows={2} value={form.startup?.command ?? ""} onChange={(v) => upd((d) => { d.startup = { ...d.startup, command: v }; })} placeholder="./server -port {{PORT_GAME}}" /></Field>
        <Field grow><Input label="READY REGEX (OPTIONAL)" mono value={form.startup?.ready_regex ?? ""} onChange={(e) => upd((d) => { d.startup = { ...d.startup, ready_regex: e.target.value || undefined }; })} placeholder="Connection to Steam servers successful" /></Field>
        <Row>
          <Field><Select label="STOP TYPE" value={form.startup?.stop?.type ?? "signal"} options={["signal", "command"]} onChange={(v) => upd((d) => { d.startup = { ...d.startup, stop: { ...d.startup?.stop, type: v } }; })} /></Field>
          <Field grow><Input label="STOP VALUE" mono value={form.startup?.stop?.value ?? ""} onChange={(e) => upd((d) => { d.startup = { ...d.startup, stop: { ...d.startup?.stop, value: e.target.value } }; })} placeholder="SIGINT  /  quit" /></Field>
        </Row>
      </Section>

      <Section title="Variables" onAdd={() => upd((d) => { (d.variables ??= []).push({ key: "", default: "", user_editable: true }); })}>
        {variables.length === 0 && <Empty>No launch variables.</Empty>}
        {variables.map((v, i) => (
          <Row key={i} onRemove={() => upd((d) => d.variables.splice(i, 1))}>
            <Field><Input label="KEY" mono value={v.key ?? ""} onChange={(e) => upd((d) => (d.variables[i].key = e.target.value))} placeholder="MAX_PLAYERS" /></Field>
            <Field><Input label="LABEL" value={v.label ?? ""} onChange={(e) => upd((d) => (d.variables[i].label = e.target.value || undefined))} /></Field>
            <Field><Input label="DEFAULT" mono value={v.default ?? ""} onChange={(e) => upd((d) => (d.variables[i].default = e.target.value))} /></Field>
            <Field><Input label="RULES" mono value={v.rules ?? ""} onChange={(e) => upd((d) => (d.variables[i].rules = e.target.value || undefined))} placeholder="int|min:1|max:64" /></Field>
            <Field><BoolField label="EDITABLE" checked={!!v.user_editable} onChange={(val) => upd((d) => (d.variables[i].user_editable = val))} /></Field>
          </Row>
        ))}
      </Section>

      <Section title="Ports" onAdd={() => upd((d) => { (d.ports ??= []).push({ name: "", protocol: "udp", default: 0, required: false }); })}>
        {ports.length === 0 && <Empty>No ports — add at least one.</Empty>}
        {ports.map((p, i) => (
          <Row key={i} onRemove={() => upd((d) => d.ports.splice(i, 1))}>
            <Field><Input label="NAME" mono value={p.name ?? ""} onChange={(e) => upd((d) => (d.ports[i].name = e.target.value))} placeholder="game" /></Field>
            <Field><Select label="PROTOCOL" value={p.protocol ?? "udp"} options={["tcp", "udp"]} onChange={(v) => upd((d) => (d.ports[i].protocol = v))} /></Field>
            <Field><Num label="DEFAULT" value={p.default} onChange={(v) => upd((d) => (d.ports[i].default = v))} /></Field>
            <Field><BoolField label="REQUIRED" checked={!!p.required} onChange={(v) => upd((d) => (d.ports[i].required = v))} /></Field>
          </Row>
        ))}
      </Section>

      <Section title="Resources">
        <Row>
          <Field><Num label="MIN MEMORY (MB)" value={form.resources?.min_memory_mb} onChange={(v) => upd((d) => { d.resources = { ...d.resources, min_memory_mb: v }; })} /></Field>
          <Field><Num label="RECOMMENDED (MB)" value={form.resources?.recommended_memory_mb} onChange={(v) => upd((d) => { d.resources = { ...d.resources, recommended_memory_mb: v || undefined }; })} /></Field>
        </Row>
      </Section>

      <Section title="Settings groups" onAdd={() => upd((d) => { (d.settings ??= {}).groups ??= []; d.settings.groups.push({ id: "", label: "", fields: [] }); })}>
        {groups.length === 0 && <Empty>No settings groups. These render game options into config files.</Empty>}
        {groups.map((g, gi) => (
          <div key={gi} style={{ border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-md)", padding: 14, background: "var(--bg-inset)" }}>
            <Row onRemove={() => upd((d) => d.settings.groups.splice(gi, 1))}>
              <Field><Input label="GROUP ID" mono value={g.id ?? ""} onChange={(e) => upd((d) => (d.settings.groups[gi].id = e.target.value))} placeholder="world" /></Field>
              <Field grow><Input label="LABEL" value={g.label ?? ""} onChange={(e) => upd((d) => (d.settings.groups[gi].label = e.target.value || undefined))} placeholder="World" /></Field>
            </Row>
            <div style={{ marginTop: 10, display: "grid", gap: 8 }}>
              {(g.fields ?? []).map((f: any, fi: number) => (
                <Row key={fi} onRemove={() => upd((d) => d.settings.groups[gi].fields.splice(fi, 1))}>
                  <Field><Input label="KEY" mono value={f.key ?? ""} onChange={(e) => upd((d) => (d.settings.groups[gi].fields[fi].key = e.target.value))} placeholder="max_players" /></Field>
                  <Field><Input label="LABEL" value={f.label ?? ""} onChange={(e) => upd((d) => (d.settings.groups[gi].fields[fi].label = e.target.value || undefined))} /></Field>
                  <Field><Select label="TYPE" value={f.type ?? "string"} options={FIELD_TYPES} onChange={(v) => upd((d) => (d.settings.groups[gi].fields[fi].type = v))} /></Field>
                  <Field><Input label="DEFAULT" mono value={f.default ?? ""} onChange={(e) => upd((d) => (d.settings.groups[gi].fields[fi].default = e.target.value))} /></Field>
                  {f.type === "enum" && (
                    <Field><Input label="OPTIONS (COMMA)" value={(f.options ?? []).join(", ")} onChange={(e) => upd((d) => (d.settings.groups[gi].fields[fi].options = e.target.value.split(",").map((s) => s.trim()).filter(Boolean)))} /></Field>
                  )}
                </Row>
              ))}
              <Button size="sm" variant="ghost" icon="plus" onClick={() => upd((d) => { (d.settings.groups[gi].fields ??= []).push({ key: "", type: "string", default: "" }); })}>
                Add field
              </Button>
            </div>
          </div>
        ))}
      </Section>

      <div style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-faint)", padding: "4px 2px" }}>
        Note: <code style={{ color: "var(--text-muted)" }}>config_files</code> (bindings / templates) are preserved but edited in the{" "}
        <strong style={{ color: "var(--text-secondary)" }}>Code</strong> view.
      </div>
    </div>
  );
}

/* ---------- small UI primitives ---------- */

const fieldLabel: React.CSSProperties = {
  display: "block", fontFamily: mono, fontSize: 10, letterSpacing: "1px", color: "var(--text-faint)", marginBottom: 6, textTransform: "uppercase",
};
const controlStyle: React.CSSProperties = {
  width: "100%", padding: "9px 11px", borderRadius: "var(--radius-sm)", background: "var(--bg-inset)",
  color: "var(--text-primary)", border: "1px solid var(--border-subtle)", fontSize: 13,
  fontFamily: "var(--font-sans)", outline: "none",
};

function Section({ title, children, onAdd }: { title: string; children: React.ReactNode; onAdd?: () => void }) {
  return (
    <Card padding={18}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 12 }}>
        <div style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" }}>{title.toUpperCase()}</div>
        {onAdd && <Button size="sm" variant="secondary" icon="plus" onClick={onAdd}>Add</Button>}
      </div>
      <div style={{ display: "grid", gap: 12 }}>{children}</div>
    </Card>
  );
}

function Row({ children, onRemove }: { children: React.ReactNode; onRemove?: () => void }) {
  return (
    <div style={{ display: "flex", gap: 12, alignItems: "flex-end", flexWrap: "wrap" }}>
      <div style={{ display: "flex", gap: 12, flex: 1, flexWrap: "wrap", minWidth: 0 }}>{children}</div>
      {onRemove && <IconButton icon="x" size="sm" variant="ghost" title="Remove" onClick={onRemove} style={{ flex: "none" }} />}
    </div>
  );
}

function Field({ children, grow }: { children: React.ReactNode; grow?: boolean }) {
  return <div style={{ flex: grow ? 2 : 1, minWidth: 120 }}>{children}</div>;
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div style={{ fontFamily: mono, fontSize: 12.5, color: "var(--text-faint)" }}>{children}</div>;
}

function Area({ label, value, onChange, placeholder, rows = 2 }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; rows?: number }) {
  return (
    <label>
      <span style={fieldLabel}>{label}</span>
      <textarea value={value} placeholder={placeholder} rows={rows} onChange={(e) => onChange(e.target.value)} style={{ ...controlStyle, fontFamily: mono, fontSize: 12.5, lineHeight: 1.5, resize: "vertical" }} />
    </label>
  );
}

function Num({ label, value, onChange }: { label: string; value: number | undefined; onChange: (v: number | undefined) => void }) {
  return (
    <Input
      label={label}
      mono
      type="number"
      value={value ?? ""}
      onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
    />
  );
}

function Select({ label, value, options, onChange }: { label: string; value: string; options: string[]; onChange: (v: string) => void }) {
  return <DSelect label={label} mono size="sm" value={value} options={options.map((o) => ({ value: o, label: o }))} onChange={onChange} />;
}

// BoolField is a labelled boolean, matched to the height of the labelled inputs
// in the same Row.
function BoolField({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ display: "block" }}>
      <span style={fieldLabel}>{label}</span>
      <div style={{ height: 38, display: "flex", alignItems: "center" }}>
        <Toggle checked={checked} onChange={onChange} />
      </div>
    </label>
  );
}

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ display: "flex", alignItems: "center", gap: 10, cursor: "pointer", fontFamily: "var(--font-sans)", fontSize: 13.5, color: "var(--text-secondary)" }}>
      <Toggle checked={checked} onChange={onChange} />
      {label}
    </label>
  );
}

function Segmented({ options, value, onChange }: { options: [string, string][]; value: string; onChange: (v: string) => void }) {
  return (
    <div style={{ display: "flex", border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-sm)", overflow: "hidden" }}>
      {options.map(([val, lbl]) => (
        <button
          key={val}
          onClick={() => onChange(val)}
          style={{
            background: value === val ? "var(--accent-wash-12)" : "transparent",
            color: value === val ? "var(--accent)" : "var(--text-secondary)",
            border: "none", fontFamily: mono, fontSize: 12, letterSpacing: "0.5px", padding: "7px 16px", cursor: "pointer",
            transition: "color var(--duration-fast) var(--ease-standard), background var(--duration-fast) var(--ease-standard)",
          }}
        >
          {lbl}
        </button>
      ))}
    </div>
  );
}
