<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { reducedMotion } from "@/lib/state.svelte";

  // Ambient abyssal backdrop: gradient ground, surface light, god rays,
  // marine snow on canvas, fog and vignette. Snow loop verbatim from the
  // mock (itself ported from kraken's AmbientBackground).
  let canvas: HTMLCanvasElement;

  $effect(() => {
    const c = canvas;
    const bctx = c.getContext("2d")!;
    // Rendered at CSS resolution on purpose. The flakes are 0.4–2px soft discs;
    // drawing them at device resolution multiplies the per-frame fill 2–4× on a
    // HiDPI panel for grain the eye cannot resolve, and the compositor's
    // upscale only softens them.
    const dpr = 1;
    const N = 78;
    // The glow used to be a canvas shadowBlur per lit flake — a separate blur
    // pass per fill, every frame. It is now one radial gradient rendered once
    // and stamped.
    const GLOW = 8;
    const glow = document.createElement("canvas");
    glow.width = glow.height = 32;
    {
      const g = glow.getContext("2d")!;
      const grad = g.createRadialGradient(16, 16, 0, 16, 16, 16);
      grad.addColorStop(0, "rgba(255,205,130,0.55)");
      grad.addColorStop(0.3, "rgba(255,205,130,0.22)");
      grad.addColorStop(1, "rgba(255,205,130,0)");
      g.fillStyle = grad;
      g.fillRect(0, 0, 32, 32);
    }
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
        if (p.glow) {
          const R = p.r + GLOW;
          bctx.drawImage(glow, p.x - R, p.y - R, R * 2, R * 2);
          bctx.fillStyle = "rgba(255,228,180," + (p.a + 0.2) + ")";
        } else {
          bctx.fillStyle = "rgba(206,196,180," + p.a + ")";
        }
        bctx.beginPath();
        bctx.arc(p.x, p.y, p.r, 0, 6.283);
        bctx.fill();
      }
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
    <div class="bg-ray" use:istyle={"left: 14%; width: 120px; height: 95vh; --ray-op: 0.16; --ray-dur: 17s; --ray-delay: 0s"}></div>
    <div class="bg-ray" use:istyle={"left: 34%; width: 80px; height: 90vh; --ray-op: 0.12; --ray-dur: 13s; --ray-delay: 1.5s"}></div>
    <div class="bg-ray" use:istyle={"left: 58%; width: 150px; height: 96vh; --ray-op: 0.14; --ray-dur: 21s; --ray-delay: 0.8s"}></div>
    <div class="bg-ray" use:istyle={"left: 78%; width: 90px; height: 88vh; --ray-op: 0.10; --ray-dur: 15s; --ray-delay: 2.2s"}></div>
  </div>
  <canvas id="bgSnow" bind:this={canvas}></canvas>
  <div class="bg-fog">
    <div class="bg-fog-glow"></div>
    <div class="bg-vignette"></div>
  </div>
</div>
