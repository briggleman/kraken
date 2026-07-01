import { useCallback, useEffect, useRef, useState } from "react";
import Editor from "react-simple-code-editor";
import { api } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import type { FileContent, FileEntry } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Icon } from "@ds/components/core/Icon";
import { Badge } from "@ds/components/core/Badge";
import { SftpCard } from "./SftpCard";
import { highlightConfig } from "@/components/highlight";

const mono = "var(--font-mono)";
const ROOT = "/data";
const TRASH = "/data/.trash";

// trashName encodes the original path into the trashed filename so Restore can
// put it back where it came from. "/" → "·" (a char that won't appear in paths).
function trashName(origPath: string): string {
  const rel = origPath.replace(ROOT + "/", "");
  return `${Date.now()}__${rel.replace(/\//g, "·")}`;
}
function restoreTarget(trashedName: string): string {
  const rel = trashedName.replace(/^\d+__/, "").replace(/·/g, "/");
  return `${ROOT}/${rel}`;
}

function human(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MiB`;
  return `${(size / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

function friendlyDate(ms: number): string {
  if (!ms) return "—";
  const diff = Date.now() - ms;
  const min = 60_000, hr = 60 * min, day = 24 * hr;
  if (diff < min) return "just now";
  if (diff < hr) return `${Math.floor(diff / min)} min ago`;
  if (diff < day) return `about ${Math.floor(diff / hr)} hours ago`;
  if (diff < 7 * day) return `${Math.floor(diff / day)} days ago`;
  return new Date(ms).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

const GRID = "36px 1fr 130px 200px 44px";

const colHead: React.CSSProperties = {
  display: "grid",
  gridTemplateColumns: GRID,
  padding: "10px 16px",
  borderBottom: "1px solid var(--border-subtle)",
  fontFamily: mono,
  fontSize: 10.5,
  letterSpacing: 1,
  textTransform: "uppercase",
  color: "var(--text-faint)",
};

export function ServerFilesPanel({ id, name }: { id: string; name: string }) {
  const { confirm, prompt } = useDialog();
  const [path, setPath] = useState(ROOT);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<FileEntry | null>(null);
  const uploadRef = useRef<HTMLInputElement | null>(null);

  const load = useCallback((p: string) => {
    setLoading(true);
    setError(null);
    setSelected(new Set());
    setMenuFor(null);
    api.listFiles(id, p)
      .then((res) => {
        setEntries((res.entries ?? []).slice().sort((a, b) => (a.is_dir === b.is_dir ? a.name.localeCompare(b.name) : a.is_dir ? -1 : 1)));
        setPath(res.path || p);
      })
      .catch((e) => setError(e instanceof Error ? e.message : "failed to list files"))
      .finally(() => setLoading(false));
  }, [id]);

  useEffect(() => { load(ROOT); }, [load]);

  const reload = () => load(path);

  const toggle = (p: string) => setSelected((prev) => {
    const next = new Set(prev);
    next.has(p) ? next.delete(p) : next.add(p);
    return next;
  });
  // Hide the trash directory from the normal /data listing.
  const visible = entries.filter((e) => !(path === ROOT && e.path === TRASH));
  const selectedEntries = entries.filter((e) => selected.has(e.path));
  const allSelected = visible.length > 0 && selected.size === visible.length;
  const toggleAll = () => setSelected(allSelected ? new Set() : new Set(visible.map((e) => e.path)));

  const goUp = () => {
    if (path === ROOT) return;
    const parent = path.slice(0, path.lastIndexOf("/"));
    load(parent.length < ROOT.length ? ROOT : parent);
  };

  const wrap = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try { await fn(); reload(); }
    catch (e) { setError(e instanceof Error ? e.message : "operation failed"); setBusy(false); }
  };

  const newDir = async () => {
    const n = await prompt({ title: "New directory", message: "New directory name:", placeholder: "configs" });
    if (n) void wrap(() => api.mkdir(id, `${path}/${n}`));
  };
  const newFile = async () => {
    const n = await prompt({ title: "New file", message: "New file name:", placeholder: "server.cfg" });
    if (n) void wrap(() => api.writeFile(id, `${path}/${n}`, ""));
  };
  const onUpload = (files: FileList | null) => {
    if (files && files.length) void wrap(() => api.uploadFiles(id, path, Array.from(files)));
  };
  const inTrash = path === TRASH || path.startsWith(TRASH + "/");

  const del = async (paths: string[]) => {
    if (paths.length && (await confirm({ title: "Delete files", message: `Permanently delete ${paths.length} item(s)? This cannot be undone.`, confirmLabel: "Delete", danger: true }))) {
      void wrap(() => api.deleteFiles(id, paths));
    }
  };
  // Soft-delete: move items into the trash dir (one move per item).
  const trash = (entries: FileEntry[]) => {
    setMenuFor(null);
    if (!entries.length) return;
    void wrap(async () => {
      for (const e of entries) await api.moveFile(id, e.path, `${TRASH}/${trashName(e.path)}`);
    });
  };
  const restore = (entries: FileEntry[]) => {
    setMenuFor(null);
    void wrap(async () => {
      for (const e of entries) await api.moveFile(id, e.path, restoreTarget(e.name));
    });
  };
  const rename = async (e: FileEntry) => {
    setMenuFor(null);
    const n = await prompt({ title: "Rename", message: "Rename to:", defaultValue: e.name });
    if (n && n !== e.name) void wrap(() => api.moveFile(id, e.path, `${path}/${n}`));
  };
  const copy = async (e: FileEntry) => {
    setMenuFor(null);
    const dot = e.name.lastIndexOf(".");
    const suggested = !e.is_dir && dot > 0 ? `${e.name.slice(0, dot)}-copy${e.name.slice(dot)}` : `${e.name}-copy`;
    const n = await prompt({ title: "Copy", message: "Copy to:", defaultValue: suggested });
    if (n && n !== e.name) void wrap(() => api.copyFile(id, e.path, `${path}/${n}`));
  };
  const saveBlob = (blob: Blob, filename: string) => {
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url; a.download = filename;
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);
  };
  const download = async (paths: string[]) => {
    setMenuFor(null);
    try {
      saveBlob(await api.downloadFilesZip(id, paths), `${name}-files.zip`);
    } catch (e) {
      setError(e instanceof Error ? e.message : "download failed");
    }
  };
  // Single files download as their raw bytes (with their real name); directories
  // and multi-select still go through the zip path.
  const downloadRaw = async (p: string) => {
    setMenuFor(null);
    try {
      saveBlob(await api.downloadFileRaw(id, p), p.split("/").pop() || "file");
    } catch (e) {
      setError(e instanceof Error ? e.message : "download failed");
    }
  };

  const segs = path.replace(ROOT, "").split("/").filter(Boolean);

  return (
    <div style={{ paddingBottom: 30 }}>
      <SftpCard id={id} />
      <input ref={uploadRef} type="file" multiple style={{ display: "none" }} onChange={(e) => { onUpload(e.target.files); e.target.value = ""; }} />

      {/* breadcrumb + actions */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 14, gap: 16, flexWrap: "wrap" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "7px 12px", borderRadius: "var(--radius-pill)", border: "1px dashed rgba(61,245,207,.2)", fontFamily: mono, fontSize: 13.5, color: "var(--text-muted)", flexWrap: "wrap" }}>
          <input type="checkbox" checked={allSelected} onChange={toggleAll} title="Select all" style={{ accentColor: "var(--accent)" }} />
          <span onClick={() => load(ROOT)} style={{ cursor: "pointer", color: path === ROOT ? "var(--accent)" : "var(--text-secondary)" }}>data</span>
          {segs.map((seg, i) => {
            const target = ROOT + "/" + segs.slice(0, i + 1).join("/");
            const last = i === segs.length - 1;
            return (
              <span key={target} style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                <span style={{ opacity: 0.5 }}>/</span>
                <span onClick={() => load(target)} style={{ cursor: "pointer", color: last ? "var(--accent)" : "var(--text-secondary)" }}>{seg}</span>
              </span>
            );
          })}
        </div>
        <div style={{ display: "flex", gap: 10 }}>
          {path !== ROOT && <Button size="sm" variant="ghost" onClick={goUp}>↑ Up</Button>}
          {!inTrash && <Button size="sm" variant="ghost" icon="x" onClick={() => load(TRASH)}>Trash</Button>}
          {!inTrash && <Button size="sm" variant="secondary" icon="folder" onClick={newDir}>New directory</Button>}
          {!inTrash && <Button size="sm" variant="secondary" icon="plus" onClick={() => uploadRef.current?.click()}>Upload</Button>}
          {!inTrash && <Button size="sm" variant="primary" icon="file" onClick={newFile}>New file</Button>}
        </div>
      </div>

      {/* selection bar */}
      {selected.size > 0 && (
        <div style={{ display: "flex", alignItems: "center", gap: 14, marginBottom: 12, padding: "10px 14px", borderRadius: "var(--radius-md)", border: "1px dashed rgba(61,245,207,.2)", background: "var(--accent-wash-08)", fontFamily: mono, fontSize: 13 }}>
          <span style={{ color: "var(--accent)" }}>{selected.size} selected</span>
          {inTrash ? (
            <>
              <Button size="sm" variant="secondary" icon="refresh" onClick={() => restore(selectedEntries)}>Restore</Button>
              <Button size="sm" variant="danger" icon="x" onClick={() => del([...selected])}>Delete forever</Button>
            </>
          ) : (
            <>
              <Button size="sm" variant="secondary" icon="copy" onClick={() => download([...selected])}>Download .zip</Button>
              <Button size="sm" variant="danger" icon="x" onClick={() => trash(selectedEntries)}>Move to Trash</Button>
            </>
          )}
          <span onClick={() => setSelected(new Set())} style={{ cursor: "pointer", color: "var(--text-muted)" }}>clear</span>
        </div>
      )}

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 12 }}>{error}</div>}

      {loading ? (
        <div style={{ padding: 24, fontFamily: mono, color: "var(--text-muted)", border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-lg)", background: "var(--bg-inset)" }}>Loading…</div>
      ) : visible.length === 0 ? (
        <Card dashed padding={36}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 10, textAlign: "center" }}>
            <span style={{ fontSize: 30 }}>🐙</span>
            <div style={{ fontFamily: mono, color: "var(--text-muted)" }}>{inTrash ? "Trash is empty." : "Empty directory."}</div>
          </div>
        </Card>
      ) : (
      <div style={{ border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-lg)", background: "var(--bg-inset)", overflow: "visible" }}>
        <div style={colHead}>
          <span /><span>Name</span><span style={{ textAlign: "right" }}>Size</span><span style={{ textAlign: "right" }}>Modified</span><span />
        </div>

        {visible.map((e) => (
            <div key={e.path} className="file-row" style={{ display: "grid", gridTemplateColumns: GRID, alignItems: "center", padding: "10px 16px", borderBottom: "1px solid var(--border-subtle)", fontSize: 13.5, position: "relative" }}>
              <input type="checkbox" checked={selected.has(e.path)} onChange={() => toggle(e.path)} style={{ accentColor: "var(--accent)" }} />
              <span onClick={() => (e.is_dir ? load(e.path) : setEditing(e))} style={{ display: "flex", alignItems: "center", gap: 9, cursor: "pointer", fontFamily: mono, color: "var(--text-primary)", minWidth: 0 }}>
                <Icon name={e.is_dir ? "folder" : "file"} size={16} strokeWidth={2} style={{ color: e.is_dir ? "var(--accent)" : "var(--text-muted)", flexShrink: 0 }} />
                <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {inTrash ? e.name.replace(/^\d+__/, "").replace(/·/g, "/") : e.name}
                </span>
              </span>
              <span style={{ textAlign: "right", fontFamily: mono, fontSize: 12, color: "var(--text-muted)" }}>{e.is_dir ? "" : human(e.size)}</span>
              <span style={{ textAlign: "right", fontFamily: mono, fontSize: 12, color: "var(--text-faint)" }}>{friendlyDate(e.modified_ms)}</span>
              <span style={{ display: "flex", justifyContent: "flex-end", position: "relative" }}>
                <button onClick={() => setMenuFor(menuFor === e.path ? null : e.path)} aria-label="Actions" style={{ background: "transparent", border: "none", color: "var(--text-muted)", cursor: "pointer", padding: "2px 6px", borderRadius: 6, fontSize: 18, lineHeight: 1, fontFamily: mono }}>
                  ⋮
                </button>
                {menuFor === e.path && (
                  <>
                    <div onClick={() => setMenuFor(null)} style={{ position: "fixed", inset: 0, zIndex: 50 }} />
                    <div style={{ position: "absolute", top: 28, right: 0, zIndex: 51, minWidth: 160, background: "var(--bg-raised)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-md)", boxShadow: "var(--elevation-e2)", overflow: "hidden" }}>
                      {inTrash ? (
                        <>
                          <MenuItem icon="refresh" label="Restore" onClick={() => restore([e])} />
                          <MenuItem icon="x" label="Delete forever" danger onClick={() => { setMenuFor(null); del([e.path]); }} />
                        </>
                      ) : (
                        <>
                          {!e.is_dir && <MenuItem icon="file" label="Edit" onClick={() => { setMenuFor(null); setEditing(e); }} />}
                          <MenuItem icon="copy" label={e.is_dir ? "Download .zip" : "Download"} onClick={() => (e.is_dir ? download([e.path]) : downloadRaw(e.path))} />
                          <MenuItem icon="file" label="Rename" onClick={() => rename(e)} />
                          <MenuItem icon="copy" label="Copy" onClick={() => copy(e)} />
                          <MenuItem icon="x" label="Move to Trash" onClick={() => trash([e])} />
                        </>
                      )}
                    </div>
                  </>
                )}
              </span>
            </div>
          ))}
      </div>
      )}

      {busy && <div style={{ marginTop: 10, fontFamily: mono, fontSize: 11.5, color: "var(--text-muted)" }}>Working…</div>}

      {editing && (
        <FileEditor
          id={id}
          entry={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); }}
          onDownload={() => downloadRaw(editing.path)}
        />
      )}
    </div>
  );
}

function FileEditor({ id, entry, onClose, onSaved, onDownload }: {
  id: string;
  entry: FileEntry;
  onClose: () => void;
  onSaved: () => void;
  onDownload: () => void;
}) {
  const { confirm } = useDialog();
  const [meta, setMeta] = useState<FileContent | null>(null);
  const [text, setText] = useState("");
  const [dirty, setDirty] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    setLoading(true);
    api.readFile(id, entry.path)
      .then((c) => { if (!active) return; setMeta(c); setText(c.content); })
      .catch((e) => { if (active) setErr(e instanceof Error ? e.message : "failed to read file"); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [id, entry.path]);

  const save = async () => {
    setSaving(true);
    setErr(null);
    try {
      await api.writeFile(id, entry.path, text);
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "save failed");
      setSaving(false);
    }
  };

  const tryClose = async () => {
    if (dirty && !(await confirm({ title: "Discard changes", message: "Discard unsaved changes?", confirmLabel: "Discard", danger: true }))) return;
    onClose();
  };

  const editable = !loading && !err && meta && !meta.is_binary && !meta.too_large;
  const lines = text ? text.split("\n").length : 0;

  return (
    <div onClick={tryClose} style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", display: "flex", alignItems: "center", justifyContent: "center", padding: 28 }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: "100%", maxWidth: 960, height: "84vh", display: "flex", flexDirection: "column", background: "var(--bg-raised)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-lg)", boxShadow: "var(--elevation-e3)", overflow: "hidden" }}>
        {/* header */}
        <div style={{ display: "flex", alignItems: "center", gap: 12, padding: "14px 18px", borderBottom: "1px solid var(--border-subtle)" }}>
          <Icon name="file" size={16} strokeWidth={2} style={{ color: "var(--accent)" }} />
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ fontFamily: mono, fontSize: 14, color: "var(--text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{entry.name}{dirty ? " •" : ""}</div>
            <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{entry.path}</div>
          </div>
          <button onClick={tryClose} aria-label="Close" style={{ background: "transparent", border: "none", color: "var(--text-muted)", cursor: "pointer", padding: 4, display: "inline-flex" }}>
            <Icon name="x" size={18} strokeWidth={2} />
          </button>
        </div>

        {/* body */}
        <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
          {loading ? (
            <div style={{ padding: 28, fontFamily: mono, color: "var(--text-muted)" }}>Loading…</div>
          ) : err ? (
            <div style={{ padding: 28, fontFamily: mono, fontSize: 13, color: "var(--status-crashed)" }}>{err}</div>
          ) : meta && meta.is_binary ? (
            <Guard message="This file appears to be binary and can't be edited here." onDownload={onDownload} />
          ) : meta && meta.too_large ? (
            <Guard message={`This file is ${human(meta.size)} — too large to edit in the browser (limit 1 MiB).`} onDownload={onDownload} />
          ) : (
            <div style={{ flex: 1, minHeight: 0, overflow: "auto", background: "var(--bg-inset)" }}>
              <Editor
                value={text}
                onValueChange={(v) => { setText(v); setDirty(true); }}
                highlight={highlightConfig}
                padding={{ top: 16, right: 18, bottom: 16, left: 18 }}
                textareaId="file-editor"
                style={{
                  fontFamily: mono,
                  fontSize: 13,
                  lineHeight: 1.6,
                  minHeight: "100%",
                  color: "var(--text-primary)",
                }}
              />
            </div>
          )}
        </div>

        {/* footer */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, padding: "12px 18px", borderTop: "1px solid var(--border-subtle)" }}>
          <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)" }}>
            {editable ? `${lines} lines · ${human(text.length)}` : ""}
          </div>
          <div style={{ display: "flex", gap: 10 }}>
            <Button variant="ghost" onClick={tryClose}>Cancel</Button>
            {editable && (
              <Button variant="primary" icon="check" disabled={!dirty || saving} onClick={() => void save()}>{saving ? "Saving…" : "Save"}</Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function Guard({ message, onDownload }: { message: string; onDownload: () => void }) {
  return (
    <div style={{ flex: 1, display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 16, padding: 28, textAlign: "center" }}>
      <Badge tone="coral">cannot edit</Badge>
      <div style={{ fontFamily: mono, fontSize: 13.5, color: "var(--text-muted)", maxWidth: 420 }}>{message}</div>
      <Button variant="secondary" icon="copy" onClick={onDownload}>Download instead</Button>
    </div>
  );
}

function MenuItem({ icon, label, onClick, danger }: { icon: "refresh" | "x" | "file" | "copy"; label: string; onClick: () => void; danger?: boolean }) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "flex", alignItems: "center", gap: 9, width: "100%", padding: "10px 14px",
        background: "transparent", border: "none", cursor: "pointer", textAlign: "left",
        color: danger ? "var(--status-crashed)" : "var(--text-secondary)", fontSize: 13, fontFamily: mono,
      }}
    >
      <Icon name={icon} size={14} strokeWidth={2} />{label}
    </button>
  );
}
