<!--
  SpawnWizardV9 — composed v0.9 SpawnWizard (#192, architect pivot 2026-05-27).

  Composes WizardStepper + Stage1Shape + Stage2Repo + Stage3Agents +
  Stage4Launch into a single 4-step flow. Stage 3 is renamed Members→
  Agents and uses skill chips + always-resume (no mode picker).

  Props:
    onclose():   called when operator hits ✕ or after Launch
-->
<script>
  import WizardStepper from './WizardStepper.svelte';
  import Stage1Shape from './Stage1Shape.svelte';
  import Stage2Repo from './Stage2Repo.svelte';
  import Stage3Agents from './Stage3Agents.svelte';
  import Stage4Launch from './Stage4Launch.svelte';

  let { onclose } = $props();

  let current = $state(1);
  let templateId = $state('');
  let template = $state(null);
  let repo = $state(null);
  let agents = $state([]);
  let teamName = $state('');
  let saveAsRecipe = $state(false);

  function next() { if (current < 4) current += 1; }
  function back() { if (current > 1) current -= 1; }
  function jumpTo(step) { if (step >= 1 && step < current) current = step; }

  const canAdvance = $derived.by(() => {
    switch (current) {
      case 1: return !!templateId;
      case 2: return !!repo;
      case 3: return agents.length > 0 && !!teamName;
      case 4: return true;
      default: return false;
    }
  });

  function selectTemplate(id, t) {
    templateId = id;
    template = t;
    // Reset agents when template changes
    if (!template || !template.slots || template.slots.length === 0) {
      agents = [];
    }
  }

  function launchedThenClose() { onclose?.(); }

  // Per architect pivot 2026-05-27 — stepper label "Members" → "Agents".
  const stepLabels = [
    { label: 'Shape' },
    { label: 'Repo' },
    { label: 'Agents' },
    { label: 'Launch' },
  ];
</script>

<div class="wiz">
  <header class="head">
    <h1>+ Spawn workspace</h1>
    <button class="x" onclick={() => onclose?.()} aria-label="close">×</button>
  </header>

  <WizardStepper steps={stepLabels} current={current} onnavigate={jumpTo} />

  <div class="body">
    {#if current === 1}
      <Stage1Shape bind:selectedId={templateId} onselect={selectTemplate} />
    {:else if current === 2}
      <Stage2Repo bind:selectedRepo={repo} />
    {:else if current === 3}
      <Stage3Agents
        template={template}
        bind:agents
        bind:teamName
      />
    {:else if current === 4}
      <Stage4Launch
        selection={{ template, repo, members: agents, teamName }}
        bind:saveAsRecipe
        onlaunch={launchedThenClose}
      />
    {/if}
  </div>

  <footer class="foot">
    <button type="button" class="back" onclick={back} disabled={current === 1}>← Back</button>
    <button type="button" class="cancel" onclick={() => onclose?.()}>Cancel</button>
    {#if current < 4}
      <button type="button" class="next" onclick={next} disabled={!canAdvance}>Next →</button>
    {/if}
  </footer>
</div>

<style>
  .wiz {
    background: #0a0a0a;
    color: #f5f5f5;
    border: 1px solid #2a2a2a;
    border-radius: 10px;
    overflow: hidden;
    width: 760px;
    max-width: 96vw;
    max-height: 92vh;
    display: flex; flex-direction: column;
  }
  .head { display: flex; align-items: center; padding: 0.65rem 1rem; border-bottom: 1px solid #2a2a2a; }
  .head h1 { flex: 1; font-size: 1rem; margin: 0; }
  .x { background: transparent; border: 0; color: #888; font-size: 1.2rem; cursor: pointer; padding: 0 0.4rem; }
  .x:hover { color: #e74c3c; }
  .body { flex: 1; overflow-y: auto; }
  .foot { display: flex; gap: 0.5rem; padding: 0.85rem 1rem; border-top: 1px solid #2a2a2a; }
  .back, .cancel, .next {
    padding: 0.5rem 1rem; border-radius: 6px; font-weight: 600; cursor: pointer; font: inherit; border: 0;
  }
  .back { background: transparent; color: #87ceeb; border: 1px solid #2a2a2a; }
  .back:disabled { opacity: 0.35; cursor: not-allowed; }
  .cancel { background: transparent; color: #888; }
  .next { background: #87ceeb; color: #0a0a0a; margin-left: auto; }
  .next:disabled { opacity: 0.35; cursor: not-allowed; }
</style>
