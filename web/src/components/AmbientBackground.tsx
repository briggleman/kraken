import { useEffect, useRef } from "react";

type Atmosphere = "subtle" | "balanced" | "heavy";

const RAY_OPACITY: Record<Atmosphere, number> = { subtle: 0.4, balanced: 0.7, heavy: 1 };
const FOG_OPACITY: Record<Atmosphere, number> = { subtle: 0.45, balanced: 0.7, heavy: 0.95 };
const PARTICLE_COUNT: Record<Atmosphere, number> = { subtle: 35, balanced: 78, heavy: 150 };

/**
 * The signature abyssal backdrop: a fixed depth gradient, drifting god-ray
 * columns, a marine-snow particle canvas, and a fog/vignette. Purely decorative
 * (pointer-events:none) and sits behind app content. Ported from the Abyssal
 * reference app.
 */
export function AmbientBackground({ atmosphere = "balanced" }: { atmosphere?: Atmosphere }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const c = canvasRef.current;
    if (!c) return;
    const ctx = c.getContext("2d");
    if (!ctx) return;

    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const n = PARTICLE_COUNT[atmosphere];

    let w = 0;
    let h = 0;
    let raf = 0;
    const parts: {
      x: number; y: number; r: number; vy: number; sway: number; ph: number; a: number; glow: boolean;
    }[] = [];

    const mk = () => ({
      x: Math.random() * w,
      y: Math.random() * h,
      r: Math.random() * 1.6 + 0.4,
      vy: Math.random() * 0.22 + 0.04,
      sway: Math.random() * 0.5 + 0.15,
      ph: Math.random() * 6.28,
      a: Math.random() * 0.45 + 0.12,
      glow: Math.random() < 0.22,
    });

    const resize = () => {
      w = c.clientWidth;
      h = c.clientHeight;
      c.width = Math.max(1, w * dpr);
      c.height = Math.max(1, h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      if (parts.length === 0) for (let i = 0; i < n; i++) parts.push(mk());
    };
    resize();
    window.addEventListener("resize", resize);

    const tick = () => {
      ctx.clearRect(0, 0, w, h);
      for (const p of parts) {
        p.y += p.vy;
        p.ph += 0.012;
        p.x += Math.sin(p.ph) * p.sway * 0.3;
        if (p.y > h + 4) {
          p.y = -4;
          p.x = Math.random() * w;
        }
        ctx.beginPath();
        if (p.glow) {
          ctx.shadowBlur = 8;
          ctx.shadowColor = "rgba(61,245,207,0.9)";
          ctx.fillStyle = `rgba(120,255,224,${p.a + 0.2})`;
        } else {
          ctx.shadowBlur = 0;
          ctx.fillStyle = `rgba(150,220,210,${p.a})`;
        }
        ctx.arc(p.x, p.y, p.r, 0, 6.283);
        ctx.fill();
      }
      ctx.shadowBlur = 0;
      if (!reduce) raf = requestAnimationFrame(tick);
    };
    tick();

    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("resize", resize);
    };
  }, [atmosphere]);

  const ray = (left: string, width: number, height: string, dur: number, delay: number, op: number) => (
    <div
      style={{
        position: "fixed",
        top: "-12vh",
        left,
        width,
        height,
        zIndex: 1,
        pointerEvents: "none",
        background: `linear-gradient(180deg, rgba(95,250,223,${op}), transparent 70%)`,
        filter: "blur(20px)",
        mixBlendMode: "screen",
        transform: "skewX(-9deg)",
        transformOrigin: "top center", // sway/scale pivot from the surface
        animation: `rayDrift ${dur}s ease-in-out ${delay}s infinite backwards`,
      }}
    />
  );

  return (
    <div aria-hidden style={{ opacity: 1 }}>
      {/* depth gradient (static base) */}
      <div
        style={{
          position: "fixed",
          inset: 0,
          zIndex: 0,
          pointerEvents: "none",
          background: "var(--gradient-depth)",
        }}
      />
      {/* surface light — the glow shining in from the top, gently breathing */}
      <div
        style={{
          position: "fixed",
          inset: 0,
          zIndex: 0,
          pointerEvents: "none",
          transformOrigin: "50% 0%",
          background:
            "radial-gradient(120% 80% at 50% -10%, rgba(95,250,223,.13), rgba(61,245,207,.04) 30%, transparent 55%)",
          animation: "surfaceShimmer 9s ease-in-out 0s infinite backwards",
        }}
      />
      {/* god rays */}
      <div style={{ opacity: RAY_OPACITY[atmosphere] }}>
        {ray("14%", 120, "95vh", 17, 0, 0.16)}
        {ray("34%", 80, "90vh", 13, 1.5, 0.12)}
        {ray("58%", 150, "96vh", 21, 0.8, 0.14)}
        {ray("78%", 90, "88vh", 15, 2.2, 0.1)}
      </div>
      {/* marine snow */}
      <canvas
        ref={canvasRef}
        style={{ position: "fixed", inset: 0, zIndex: 2, pointerEvents: "none", width: "100%", height: "100%" }}
      />
      {/* fog / vignette */}
      <div style={{ opacity: FOG_OPACITY[atmosphere] }}>
        <div
          style={{
            position: "fixed",
            inset: 0,
            zIndex: 3,
            pointerEvents: "none",
            background: "radial-gradient(120% 70% at 50% 120%, rgba(3,28,33,.9), transparent 55%)",
          }}
        />
        <div
          style={{
            position: "fixed",
            inset: 0,
            zIndex: 3,
            pointerEvents: "none",
            boxShadow: "inset 0 0 320px 90px rgba(1,9,14,.9)",
          }}
        />
      </div>
    </div>
  );
}
