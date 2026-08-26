<script lang="ts">
  import { reducedMotion } from "@/lib/state.svelte";

  // Ambient abyssal backdrop: gradient ground, surface light, god rays,
  // marine snow on canvas, fog and vignette. Snow loop verbatim from the
  // mock (itself ported from kraken's AmbientBackground).
  let canvas: HTMLCanvasElement;

  $effect(() => {
    const c = canvas;
    const bctx = c.getContext("2d")!;
    const dpr = Math.min(devicePixelRatio || 1, 2);
    const N = 78;
    let w = 0,
      h = 0;
    interface Flake {
      x: number;
      y: number;
      r: number;
      vy: number;
      sway: number;
      ph: number;
      a: number;
      glow: boolean;
    }
    const parts: Flake[] = [];
    const mk = (): Flake => ({
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
      bctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      if (parts.length === 0) for (let i = 0; i < N; i++) parts.push(mk());
    };
    resize();
    addEventListener("resize", resize);
    let raf = 0;
    const tick = () => {
      bctx.clearRect(0, 0, w, h);
      for (const p of parts) {
        p.y += p.vy;
        p.ph += 0.012;
        p.x += Math.sin(p.ph) * p.sway * 0.3;
        if (p.y > h + 4) {
          p.y = -4;
          p.x = Math.random() * w;
        }
        bctx.beginPath();
        if (p.glow) {
          bctx.shadowBlur = 8;
          bctx.shadowColor = "rgba(255,205,130,0.9)";
          bctx.fillStyle = "rgba(255,228,180," + (p.a + 0.2) + ")";
        } else {
          bctx.shadowBlur = 0;
          bctx.fillStyle = "rgba(206,196,180," + p.a + ")";
        }
        bctx.arc(p.x, p.y, p.r, 0, 6.283);
        bctx.fill();
      }
      bctx.shadowBlur = 0;
      if (!reducedMotion) raf = requestAnimationFrame(tick);
    };
    tick();
    return () => {
      removeEventListener("resize", resize);
      cancelAnimationFrame(raf);
    };
  });
</script>

<div aria-hidden="true">
  <div class="bg-depth"></div>
  <div class="bg-surface-light"></div>
  <div class="bg-rays">
    <div class="bg-ray" style="left: 14%; width: 120px; height: 95vh; --ray-op: 0.16; --ray-dur: 17s; --ray-delay: 0s"></div>
    <div class="bg-ray" style="left: 34%; width: 80px; height: 90vh; --ray-op: 0.12; --ray-dur: 13s; --ray-delay: 1.5s"></div>
    <div class="bg-ray" style="left: 58%; width: 150px; height: 96vh; --ray-op: 0.14; --ray-dur: 21s; --ray-delay: 0.8s"></div>
    <div class="bg-ray" style="left: 78%; width: 90px; height: 88vh; --ray-op: 0.10; --ray-dur: 15s; --ray-delay: 2.2s"></div>
  </div>
  <canvas id="bgSnow" bind:this={canvas}></canvas>
  <div class="bg-fog">
    <div class="bg-fog-glow"></div>
    <div class="bg-vignette"></div>
  </div>
</div>
