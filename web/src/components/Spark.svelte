<script lang="ts">
  // Gold area sparkline (the disk chart): faint grid, gradient fill, glowing
  // line, emphasized endpoint. Drawing code is verbatim from the mock.
  //
  // values are percentages (0-100), oldest first. The series is anchored to the
  // RIGHT edge against `capacity`, so a history still filling up draws only as
  // far left as it has samples instead of stretching a handful of points across
  // the full width and implying history that doesn't exist.
  //
  // flat: no feed behind this chart — draw a dim, still baseline instead, so
  // the readout's em-dash and the chart say the same thing.
  let {
    values = [],
    capacity,
    flat = false,
  }: { values?: number[]; capacity?: number; flat?: boolean } = $props();

  let cv: HTMLCanvasElement;
  // The box the canvas lays out to, as the ResizeObserver last reported it. A
  // redraw used to read clientWidth/clientHeight itself, and it runs right
  // after a telemetry tick has restyled every meter column on the page — so
  // each sample forced a synchronous style and layout pass. $state, so a
  // resize redraws through the same effect a new sample does.
  let box = $state({ w: 0, h: 0 });

  const span = $derived(Math.max(capacity ?? values.length, values.length, 2));
  const blank = $derived(flat || values.length === 0);

  function prepare(w: number, h: number): CanvasRenderingContext2D {
    const bw = Math.round(w * devicePixelRatio);
    const bh = Math.round(h * devicePixelRatio);
    // Assigning width or height reallocates the bitmap even when the value is
    // unchanged, so only a real size change does; otherwise clearing is enough.
    if (cv.width !== bw || cv.height !== bh) {
      cv.width = bw;
      cv.height = bh;
    }
    const c = cv.getContext("2d")!;
    c.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
    c.clearRect(0, 0, w, h);
    c.setLineDash([]);
    c.strokeStyle = "rgba(255, 194, 102, 0.08)";
    c.lineWidth = 1;
    for (let gy = 1; gy <= 2; gy++) {
      c.beginPath();
      c.moveTo(0, (h * gy) / 3);
      c.lineTo(w, (h * gy) / 3);
      c.stroke();
    }
    return c;
  }

  function draw() {
    const { w, h } = box;
    if (!w) return;
    if (blank) {
      drawFlat(w, h);
      return;
    }
    const c = prepare(w, h);
    const step = w / (span - 1);
    const pts = values.map((v, i) => [
      w - (values.length - 1 - i) * step,
      h - (Math.max(0, Math.min(100, v)) / 100) * (h - 4) - 2,
    ]);

    if (pts.length > 1) {
      const grad = c.createLinearGradient(0, 0, 0, h);
      grad.addColorStop(0, "rgba(255, 194, 102, 0.28)");
      grad.addColorStop(1, "rgba(255, 194, 102, 0)");
      c.fillStyle = grad;
      c.beginPath();
      c.moveTo(pts[0][0], h);
      pts.forEach((p) => c.lineTo(p[0], p[1]));
      c.lineTo(pts[pts.length - 1][0], h);
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
    }
    // The endpoint dot carries the reading on its own until a second sample
    // arrives and there is a line to anchor it to.
    const e = pts[pts.length - 1];
    c.fillStyle = "#ffc266";
    c.beginPath();
    c.arc(e[0], e[1], 2.5, 0, 7);
    c.fill();
  }

  function drawFlat(w: number, h: number) {
    const c = prepare(w, h);
    c.strokeStyle = "rgba(125, 113, 96, 0.55)";
    c.setLineDash([3, 4]);
    c.beginPath();
    c.moveTo(0, h - 2);
    c.lineTo(w, h - 2);
    c.stroke();
  }

  $effect(() => {
    // The observer reports the box once on observe and again on every change,
    // including the ones a window resize never announces (a band reflowing
    // into a new column count).
    const ro = new ResizeObserver(([e]) => {
      box = { w: e.contentRect.width, h: e.contentRect.height };
    });
    ro.observe(cv);
    return () => ro.disconnect();
  });

  $effect(() => {
    // Touch the reactive inputs so a new sample (or losing the feed) redraws.
    void values;
    void blank;
    void span;
    void box;
    draw();
  });
</script>

<canvas bind:this={cv} data-spark></canvas>
