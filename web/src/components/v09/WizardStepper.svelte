<!--
  WizardStepper — v0.9 wizard shared chrome (#176, #177).

  Props:
    steps:     [{label}, ...]
    current:   1-based index of the active step
    onnavigate(targetIndex): callback when operator clicks a past step

  Rules:
    - Past steps are clickable; future steps disabled
    - Current step gets a filled dot + bold label
    - Done steps get a check + dim primary color
-->
<script>
  let { steps, current, onnavigate } = $props();
</script>

<nav class="stepper" aria-label="wizard progress">
  {#each steps as step, i}
    {@const num = i + 1}
    {@const isDone = num < current}
    {@const isCurrent = num === current}
    {@const isFuture = num > current}
    <button
      class="step"
      class:done={isDone}
      class:current={isCurrent}
      class:future={isFuture}
      disabled={!isDone}
      aria-current={isCurrent ? 'step' : undefined}
      onclick={() => isDone && onnavigate?.(num)}
      type="button"
    >
      <span class="dot" aria-hidden="true">
        {#if isDone}
          <svg viewBox="0 0 16 16" width="14" height="14"><path d="M3 8.5l3 3 7-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
        {:else}
          <span class="num">{num}</span>
        {/if}
      </span>
      <span class="label">{step.label}</span>
    </button>
    {#if i < steps.length - 1}
      <span class="sep" aria-hidden="true">›</span>
    {/if}
  {/each}
</nav>

<style>
  .stepper {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.55rem 0.75rem;
    background: var(--bg-elevated, #1a1a1a);
    border-bottom: 1px solid var(--border, #2a2a2a);
    font-size: 0.85rem;
  }
  .step {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    border: 0;
    background: transparent;
    color: var(--fg-muted, #888);
    cursor: default;
    padding: 0.2rem 0.4rem;
    border-radius: 4px;
    font: inherit;
  }
  .step.done { color: var(--accent-2, #87ceeb); cursor: pointer; }
  .step.done:hover { background: rgba(135, 206, 235, 0.08); }
  .step.current { color: var(--fg, #fff); font-weight: 600; }
  .step.future { opacity: 0.5; }
  .dot {
    width: 22px; height: 22px; border-radius: 50%;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--bg, #0a0a0a);
    border: 1.5px solid currentColor;
    font-size: 0.72rem; line-height: 1;
  }
  .step.current .dot { background: var(--accent-2, #87ceeb); color: #0a0a0a; border-color: var(--accent-2, #87ceeb); }
  .num { font-weight: 600; }
  .sep { color: var(--fg-faint, #555); font-size: 0.9rem; padding: 0 0.1rem; }
</style>
