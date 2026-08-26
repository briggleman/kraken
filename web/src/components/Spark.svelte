<script lang="ts">
  import { reducedMotion } from "@/lib/state.svelte";

  // Legacy gold area sparkline (the disk chart): faint grid, gradient fill,
  // glowing line, emphasized endpoint. Verbatim drawing code from the mock.
  let { seed = 30 }: { seed?: number } = $props();

  let cv: HTMLCanvasElement;
  const data = Array.from({ length: 60 }, () => seed + (Math.random() * 10 - 5));

  function draw() {
    const w = cv.clientWidth,
      h = cv.clientHeight;
    if (!w) return;
    cv.width = w * devicePixelRatio;
    cv.height = h * devicePixelRatio;
    const c = cv.getContext("2d")!;
    c.scale(devicePixelRatio, devicePixelRatio);
    c.strokeStyle = "rgba(255, 194, 102, 0.08)";
    c.lineWidth = 1;
    for (let gy = 1; gy <= 2; gy++) {
      c.beginPath();
      c.moveTo(0, (h * gy) / 3);
      c.lineTo(w, (h * gy) / 3);
      c.stroke();
    }
    const min = 0,
      max = 100;
    const pts = data.map((v, i) => [
      (i / (data.length - 1)) * w,
      h - ((v - min) / (max - min)) * (h - 4) - 2,
    ]);
    const grad = c.createLinearGradient(0, 0, 0, h);
    grad.addColorStop(0, "rgba(255, 194, 102, 0.28)");
    grad.addColorStop(1, "rgba(255, 194, 102, 0)");
    c.fillStyle = grad;
    c.beginPath();
    c.moveTo(0, h);
    pts.forEach((p) => c.lineTo(p[0], p[1]));
    c.lineTo(w, h);
    c.closePath();
    c.fill();
    c.strokeStyle = "#ffc266";
    c.lineWidth = 1.5;
    c.shadowColor = "rgba(255, 194, 102, 0.6)";
    c.shadowBlur = 6;
    c.beginPath();
    pts.forEach((p, i) => (i ? c.lineTo(p[0], p[1]) : c.moveTo(p[0], p[1])));
    c.stroke();
    c.shadowBlur = 0;
    const e = pts[pts.length - 1];
    c.fillStyle = "#ffc266";
    c.beginPath();
    c.arc(e[0], e[1], 2.5, 0, 7);
    c.fill();
  }

  $effect(() => {
    draw();
    const onResize = () => draw();
    addEventListener("resize", onResize);
    let timer: ReturnType<typeof setInterval> | undefined;
    if (!reducedMotion) {
      timer = setInterval(() => {
        const last = data[data.length - 1];
        data.push(Math.max(2, Math.min(96, last + (Math.random() * 8 - 4))));
        data.shift();
        draw();
      }, 2000);
    }
    return () => {
      removeEventListener("resize", onResize);
      if (timer) clearInterval(timer);
    };
  });
</script>

<canvas bind:this={cv} data-spark></canvas>
