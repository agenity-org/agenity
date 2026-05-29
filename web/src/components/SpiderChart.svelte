<!--
  SpiderChart — pure-SVG radar plot. Pentagon (5 axes) reads cleanest at
  the right-pane width; the component also handles 3-8 axes gracefully.
  Each axis is { label: string, value: 0..10 }.
-->
<script>
  let { axes = [], size = 240, max = 10 } = $props();

  // Use a wider viewBox than the visual size so axis labels never clip.
  const viewW = size + 80;
  const viewH = size + 40;
  const cx = viewW / 2;
  const cy = viewH / 2 - 4;
  const r = (size / 2) * 0.65;

  function pointFor(i, n, value) {
    // -π/2 puts the first axis at the top (12 o'clock). Clockwise from there.
    const angle = -Math.PI / 2 + (i / n) * 2 * Math.PI;
    const scale = Math.max(0, Math.min(1, value / max));
    return { x: cx + Math.cos(angle) * r * scale, y: cy + Math.sin(angle) * r * scale };
  }
  function labelFor(i, n) {
    const angle = -Math.PI / 2 + (i / n) * 2 * Math.PI;
    return { x: cx + Math.cos(angle) * (r + 18), y: cy + Math.sin(angle) * (r + 14) };
  }
  function gridPoint(i, n, ratio) {
    const angle = -Math.PI / 2 + (i / n) * 2 * Math.PI;
    return { x: cx + Math.cos(angle) * r * ratio, y: cy + Math.sin(angle) * r * ratio };
  }
  function polygon(points) {
    return points.map(p => `${p.x},${p.y}`).join(' ');
  }
</script>

<svg viewBox={`0 0 ${viewW} ${viewH}`} class="spider" role="img" aria-label="Session scorecard radar chart">
  <!-- Grid: concentric pentagons at 0.25, 0.5, 0.75, 1.0 -->
  {#each [0.25, 0.5, 0.75, 1.0] as ratio (ratio)}
    <polygon
      points={polygon(axes.map((_, i) => gridPoint(i, axes.length, ratio)))}
      class="spider-grid"
      class:outer={ratio === 1.0}
    />
  {/each}

  <!-- Axis spokes -->
  {#each axes as a, i (a.label + '-spoke')}
    <line
      x1={cx} y1={cy}
      x2={gridPoint(i, axes.length, 1.0).x}
      y2={gridPoint(i, axes.length, 1.0).y}
      class="spoke"
    />
  {/each}

  <!-- Data polygon -->
  {#if axes.length >= 3}
    <polygon
      points={polygon(axes.map((a, i) => pointFor(i, axes.length, a.value)))}
      class="data"
    />
    <!-- Data points -->
    {#each axes as a, i (a.label + '-dot')}
      {@const p = pointFor(i, axes.length, a.value)}
      <circle cx={p.x} cy={p.y} r="3" class="data-dot" />
    {/each}
  {/if}

  <!-- Labels -->
  {#each axes as a, i (a.label + '-lbl')}
    {@const l = labelFor(i, axes.length)}
    <text x={l.x} y={l.y} class="label" text-anchor={l.x < cx - 4 ? 'end' : (l.x > cx + 4 ? 'start' : 'middle')}>
      {a.label}
    </text>
    <text x={l.x} y={l.y + 11} class="label-val" text-anchor={l.x < cx - 4 ? 'end' : (l.x > cx + 4 ? 'start' : 'middle')}>
      {a.value.toFixed(1)}
    </text>
  {/each}
</svg>

<style>
  .spider { display: block; width: 100%; height: auto; }
  .spider-grid { fill: none; stroke: var(--border, #1e1e1e); stroke-width: 1; }
  .spider-grid.outer { stroke: var(--border-strong, #2a2a2a); }
  .spoke { stroke: var(--border, #1e1e1e); stroke-width: 1; }
  .data { fill: var(--accent, #ffa500); fill-opacity: 0.18; stroke: var(--accent, #ffa500); stroke-width: 1.6; }
  .data-dot { fill: var(--accent, #ffa500); }
  .label { fill: var(--fg-muted, #aaa); font-size: 0.7rem; font-family: ui-sans-serif, system-ui, sans-serif; }
  .label-val { fill: var(--accent, #ffa500); font-size: 0.7rem; font-weight: 600; font-family: ui-monospace, monospace; }
</style>
