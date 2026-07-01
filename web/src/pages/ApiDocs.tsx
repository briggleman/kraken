import { useEffect, useMemo, useState } from "react";
import yaml from "js-yaml";
import { api } from "@/api/client";
import { Card } from "@ds/components/core/Card";

const mono = "var(--font-mono)";

// Method → accent color, drawn from the design system's status palette.
const METHOD_COLOR: Record<string, string> = {
  GET: "var(--status-installing)",
  POST: "var(--status-running)",
  PUT: "var(--status-starting)",
  DELETE: "var(--status-crashed)",
  PATCH: "var(--coral)",
};

interface Operation {
  method: string;
  path: string;
  summary?: string;
  description?: string;
  secured: boolean;
  params: { name: string; in: string; required?: boolean; description?: string }[];
  requestTypes: string[];
  responses: { code: string; description: string }[];
}

interface ParsedSpec {
  title: string;
  version: string;
  description?: string;
  tagOrder: string[];
  groups: Record<string, Operation[]>;
}

const METHODS = ["get", "post", "put", "patch", "delete"];

function parseSpec(doc: any): ParsedSpec {
  const groups: Record<string, Operation[]> = {};
  const tagOrder: string[] = (doc.tags ?? []).map((t: any) => t.name);
  const hasGlobalSecurity = Array.isArray(doc.security) && doc.security.length > 0;

  // Resolve a parameter that may be a $ref into components.parameters.
  const resolveParam = (p: any) => {
    if (p?.$ref) {
      const name = p.$ref.split("/").pop();
      return doc.components?.parameters?.[name] ?? {};
    }
    return p;
  };

  for (const [path, item] of Object.entries<any>(doc.paths ?? {})) {
    const pathParams = item.parameters ?? [];
    for (const method of METHODS) {
      const op = item[method];
      if (!op) continue;
      const tag = op.tags?.[0] ?? "Other";
      if (!tagOrder.includes(tag)) tagOrder.push(tag);
      // security: [] on an op disables auth (public); otherwise inherit global.
      const secured = op.security ? op.security.length > 0 : hasGlobalSecurity;

      const requestTypes = op.requestBody?.content ? Object.keys(op.requestBody.content) : [];

      const responses = Object.entries<any>(op.responses ?? {}).map(([code, r]) => ({
        code,
        description: r?.description ?? "",
      }));

      (groups[tag] ??= []).push({
        method: method.toUpperCase(),
        path,
        summary: op.summary,
        description: op.description,
        secured,
        params: [...pathParams, ...(op.parameters ?? [])].map(resolveParam).map((p: any) => ({
          name: p.name,
          in: p.in,
          required: p.required,
          description: p.description,
        })),
        requestTypes,
        responses,
      });
    }
  }
  return {
    title: doc.info?.title ?? "API",
    version: doc.info?.version ?? "",
    description: doc.info?.description,
    tagOrder,
    groups,
  };
}

function codeColor(code: string): string {
  if (code.startsWith("2")) return "var(--status-running)";
  if (code.startsWith("4")) return "var(--status-starting)";
  if (code.startsWith("5")) return "var(--status-crashed)";
  return "var(--text-muted)";
}

export function ApiDocs() {
  const [doc, setDoc] = useState<any>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});

  useEffect(() => {
    api
      .fetchOpenAPISpec()
      .then((text) => setDoc(yaml.load(text)))
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load API spec"));
  }, []);

  const spec = useMemo(() => (doc ? parseSpec(doc) : null), [doc]);

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ marginBottom: 26 }}>
        <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// DEVELOPER</div>
        <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
          API Reference
        </h1>
        {spec && (
          <p style={{ margin: "10px 0 0", fontSize: 14, color: "var(--text-muted)", maxWidth: 760, lineHeight: 1.55 }}>
            {spec.description}
          </p>
        )}
        {spec && (
          <div style={{ marginTop: 12, display: "flex", gap: 10, alignItems: "center", fontFamily: mono, fontSize: 12 }}>
            <span style={{ padding: "3px 10px", borderRadius: "var(--radius-pill)", background: "var(--accent-wash-12)", border: "1px solid var(--border-strong)", color: "var(--accent)" }}>
              v{spec.version}
            </span>
            <a href="/api/v1/openapi.yaml" style={{ color: "var(--text-secondary)", textDecoration: "underline" }}>openapi.yaml</a>
          </div>
        )}
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>{error}</div>}
      {!spec && !error && <div style={{ fontFamily: mono, color: "var(--text-muted)" }}>loading…</div>}

      {spec &&
        spec.tagOrder
          .filter((tag) => spec.groups[tag]?.length)
          .map((tag) => (
            <section key={tag} style={{ marginBottom: 30 }}>
              <h2 style={{ fontFamily: mono, fontSize: 13, letterSpacing: "2px", color: "var(--text-secondary)", textTransform: "uppercase", margin: "0 0 12px" }}>
                {tag}
              </h2>
              <Card padding={0} style={{ overflow: "hidden", background: "rgba(5,19,24,.55)" }}>
                {spec.groups[tag].map((op) => {
                  const key = op.method + op.path;
                  const isOpen = !!open[key];
                  return (
                    <div key={key} style={{ borderBottom: "1px solid var(--border-soft)" }}>
                      <button
                        onClick={() => setOpen((m) => ({ ...m, [key]: !m[key] }))}
                        style={{
                          width: "100%", display: "flex", alignItems: "center", gap: 12, padding: "12px 16px",
                          background: "transparent", border: "none", cursor: "pointer", textAlign: "left",
                        }}
                      >
                        <span style={{
                          fontFamily: mono, fontSize: 11, fontWeight: 700, letterSpacing: ".5px",
                          width: 62, textAlign: "center", padding: "4px 0", borderRadius: "var(--radius-sm)",
                          color: METHOD_COLOR[op.method] ?? "var(--text-muted)",
                          border: `1px solid ${METHOD_COLOR[op.method] ?? "var(--text-muted)"}`,
                          flex: "none",
                        }}>
                          {op.method}
                        </span>
                        <span style={{ fontFamily: mono, fontSize: 13, color: "var(--text-primary)" }}>{op.path}</span>
                        <span style={{ fontSize: 12.5, color: "var(--text-muted)", marginLeft: "auto", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", maxWidth: "45%" }}>
                          {op.summary}
                        </span>
                        {!op.secured && (
                          <span title="public — no auth required" style={{ fontFamily: mono, fontSize: 10, color: "var(--status-starting)", border: "1px solid var(--status-starting)", borderRadius: "var(--radius-sm)", padding: "2px 6px", flex: "none" }}>
                            PUBLIC
                          </span>
                        )}
                      </button>

                      {isOpen && (
                        <div style={{ padding: "4px 16px 18px 90px", fontSize: 13, color: "var(--text-secondary)" }}>
                          {op.description && <p style={{ margin: "0 0 14px", lineHeight: 1.55 }}>{op.description}</p>}

                          {op.params.length > 0 && (
                            <div style={{ marginBottom: 14 }}>
                              <div style={detailLabel}>Parameters</div>
                              {op.params.map((p) => (
                                <div key={p.in + p.name} style={{ fontFamily: mono, fontSize: 12, marginBottom: 4 }}>
                                  <span style={{ color: "var(--accent)" }}>{p.name}</span>
                                  <span style={{ color: "var(--text-faint)" }}> ({p.in}{p.required ? ", required" : ""})</span>
                                  {p.description && <span style={{ color: "var(--text-muted)" }}> — {p.description}</span>}
                                </div>
                              ))}
                            </div>
                          )}

                          {op.requestTypes.length > 0 && (
                            <div style={{ marginBottom: 14 }}>
                              <div style={detailLabel}>Request body</div>
                              <div style={{ fontFamily: mono, fontSize: 12, color: "var(--text-muted)" }}>{op.requestTypes.join(", ")}</div>
                            </div>
                          )}

                          <div>
                            <div style={detailLabel}>Responses</div>
                            {op.responses.map((r) => (
                              <div key={r.code} style={{ fontFamily: mono, fontSize: 12, marginBottom: 3 }}>
                                <span style={{ color: codeColor(r.code), fontWeight: 600 }}>{r.code}</span>
                                <span style={{ color: "var(--text-muted)" }}> — {r.description}</span>
                              </div>
                            ))}
                          </div>
                        </div>
                      )}
                    </div>
                  );
                })}
              </Card>
            </section>
          ))}
    </main>
  );
}

const detailLabel: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 10,
  letterSpacing: "1.5px",
  textTransform: "uppercase",
  color: "var(--text-faint)",
  marginBottom: 6,
};
