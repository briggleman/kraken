import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";

type DialogKind = "confirm" | "prompt" | "alert";

interface DialogOptions {
  title?: string;
  message?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  // prompt only:
  defaultValue?: string;
  placeholder?: string;
}

interface DialogRequest extends DialogOptions {
  kind: DialogKind;
  resolve: (value: boolean | string | null) => void;
}

interface DialogApi {
  /** Centered yes/no modal. Resolves true on confirm, false on cancel. */
  confirm: (opts: string | DialogOptions) => Promise<boolean>;
  /** Centered text-input modal. Resolves the string on confirm, null on cancel. */
  prompt: (opts: string | DialogOptions) => Promise<string | null>;
  /** Centered acknowledge modal. Resolves when dismissed. */
  alert: (opts: string | DialogOptions) => Promise<void>;
}

const DialogContext = createContext<DialogApi | null>(null);

function normalize(opts: string | DialogOptions): DialogOptions {
  return typeof opts === "string" ? { message: opts } : opts;
}

/**
 * DialogProvider renders Abyssal-styled, centered modal dialogs and exposes an
 * imperative, promise-based API via useDialog() — a drop-in replacement for the
 * browser's confirm()/prompt()/alert(), which render unstyled at the top of the page.
 */
export function DialogProvider({ children }: { children: ReactNode }) {
  const [req, setReq] = useState<DialogRequest | null>(null);
  const [value, setValue] = useState("");

  const open = useCallback((kind: DialogKind, opts: string | DialogOptions) => {
    const o = normalize(opts);
    setValue(o.defaultValue ?? "");
    return new Promise<boolean | string | null>((resolve) => setReq({ ...o, kind, resolve }));
  }, []);

  const api: DialogApi = {
    confirm: (o) => open("confirm", o) as Promise<boolean>,
    prompt: (o) => open("prompt", o) as Promise<string | null>,
    alert: (o) => open("alert", o).then(() => undefined),
  };

  const settle = (result: boolean | string | null) => {
    req?.resolve(result);
    setReq(null);
    setValue("");
  };

  // Cancel = false (confirm) / null (prompt); alert can't be cancelled to a value.
  const cancel = () => settle(req?.kind === "prompt" ? null : false);
  const accept = () => settle(req?.kind === "prompt" ? value : true);

  useEffect(() => {
    if (!req) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") cancel();
      else if (e.key === "Enter" && req.kind !== "alert") {
        e.preventDefault();
        accept();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [req, value]);

  return (
    <DialogContext.Provider value={api}>
      {children}
      {req && (
        <div
          onClick={cancel}
          style={{
            position: "fixed",
            inset: 0,
            zIndex: 200,
            background: "rgba(1,9,14,.78)",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            padding: 20,
            overflowY: "auto",
          }}
        >
          <div onClick={(e) => e.stopPropagation()} style={{ width: "100%", maxWidth: 440 }}>
            <Card glow padding={24}>
              {req.title && (
                <h2 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 20, letterSpacing: "-0.4px", margin: "0 0 10px", color: "var(--text-primary)" }}>
                  {req.title}
                </h2>
              )}
              {req.message && (
                <p style={{ margin: req.kind === "prompt" ? "0 0 14px" : "0 0 22px", fontSize: 14, lineHeight: 1.55, color: "var(--text-secondary)" }}>
                  {req.message}
                </p>
              )}
              {req.kind === "prompt" && (
                <Input
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  placeholder={req.placeholder}
                  mono
                  autoFocus
                  focused
                  style={{ marginBottom: 22 }}
                />
              )}
              <div style={{ display: "flex", gap: 10, justifyContent: "flex-end" }}>
                {req.kind !== "alert" && (
                  <Button variant="ghost" onClick={cancel}>
                    {req.cancelLabel ?? "Cancel"}
                  </Button>
                )}
                <Button variant={req.danger ? "danger" : "primary"} onClick={accept}>
                  {req.confirmLabel ?? (req.kind === "alert" ? "OK" : "Confirm")}
                </Button>
              </div>
            </Card>
          </div>
        </div>
      )}
    </DialogContext.Provider>
  );
}

export function useDialog(): DialogApi {
  const ctx = useContext(DialogContext);
  if (!ctx) throw new Error("useDialog must be used within DialogProvider");
  return ctx;
}
